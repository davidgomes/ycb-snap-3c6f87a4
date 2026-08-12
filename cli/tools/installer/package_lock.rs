// Copyright 2018-2026 the Deno authors. MIT license.

use std::collections::BTreeMap;
use std::collections::HashMap;
use std::collections::HashSet;
use std::path::Path;

use deno_lockfile::Lockfile;
use deno_lockfile::NpmPackageDependencyLockfileInfo;
use deno_lockfile::NpmPackageLockfileInfo;
use deno_semver::SmallStackString;
use deno_semver::StackString;
use deno_semver::Version;
use deno_semver::VersionReq;
use deno_semver::jsr::JsrDepPackageReq;
use deno_semver::package::PackageName;
use deno_semver::package::PackageReq;
use serde::Deserialize;

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct PackageLock {
  lockfile_version: u8,
  #[serde(default)]
  packages: BTreeMap<String, PackageLockPackage>,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct PackageLockPackage {
  name: Option<String>,
  version: Option<String>,
  resolved: Option<String>,
  integrity: Option<String>,
  #[serde(default)]
  link: bool,
  #[serde(default)]
  dependencies: BTreeMap<String, String>,
  #[serde(default)]
  dev_dependencies: BTreeMap<String, String>,
  #[serde(default)]
  optional_dependencies: BTreeMap<String, String>,
  #[serde(default)]
  peer_dependencies: BTreeMap<String, String>,
  #[serde(default)]
  peer_dependencies_meta: BTreeMap<String, PeerDependencyMeta>,
  #[serde(default)]
  os: Vec<String>,
  #[serde(default)]
  cpu: Vec<String>,
}

#[derive(Default, Deserialize)]
struct PeerDependencyMeta {
  optional: bool,
}

struct Package {
  name: PackageName,
  version: Version,
  resolved: Option<String>,
  integrity: String,
  dependencies: BTreeMap<String, String>,
  optional_dependencies: BTreeMap<String, String>,
  peer_dependencies: BTreeMap<String, String>,
  peer_dependencies_meta: BTreeMap<String, PeerDependencyMeta>,
  os: Vec<String>,
  cpu: Vec<String>,
}

/// Translates an npm package lockfile into a v5 Deno lockfile.
///
/// This only understands lockfile v2 and v3, whose `packages` entries model
/// the installed package tree. Invalid or unsupported package entries are
/// intentionally omitted; a normal resolution will fill them in afterwards.
pub fn package_lock_to_deno_lockfile(
  deno_lock_path: &Path,
  package_lock_text: &str,
) -> Option<String> {
  let package_lock: PackageLock =
    serde_json::from_str(package_lock_text).ok()?;
  if !matches!(package_lock.lockfile_version, 2 | 3) {
    return None;
  }

  let mut non_registry_paths = HashSet::new();
  for (path, package) in &package_lock.packages {
    for (name, specifier) in package
      .dependencies
      .iter()
      .chain(&package.dev_dependencies)
      .chain(&package.optional_dependencies)
      .chain(&package.peer_dependencies)
    {
      if is_non_registry_specifier(specifier)
        && let Some(dep_path) =
          resolve_dependency_path(path, name, &package_lock.packages)
      {
        non_registry_paths.insert(dep_path);
      }
    }
  }

  let packages = package_lock
    .packages
    .iter()
    .filter_map(|(path, package)| {
      if path.is_empty() || package.link || non_registry_paths.contains(path) {
        return None;
      }
      let name: PackageName = package
        .name
        .as_deref()
        .or_else(|| package_name_from_path(path))?
        .parse()
        .ok()?;
      let version =
        Version::parse_from_npm(package.version.as_deref()?).ok()?;
      let integrity = package.integrity.as_ref()?.trim();
      if integrity.is_empty() {
        return None;
      }
      if package.resolved.as_deref().is_some_and(|resolved| {
        !resolved.starts_with("http://") && !resolved.starts_with("https://")
      }) {
        return None;
      }
      Some((
        path.clone(),
        Package {
          name,
          version,
          resolved: package.resolved.clone(),
          integrity: integrity.to_string(),
          dependencies: package.dependencies.clone(),
          optional_dependencies: package.optional_dependencies.clone(),
          peer_dependencies: package.peer_dependencies.clone(),
          peer_dependencies_meta: package.peer_dependencies_meta.clone(),
          os: package.os.clone(),
          cpu: package.cpu.clone(),
        },
      ))
    })
    .collect::<BTreeMap<_, _>>();

  let ids = packages
    .keys()
    .map(|path| {
      (
        path.clone(),
        package_id(path, 0, &packages, &mut HashSet::new()),
      )
    })
    .collect::<HashMap<_, _>>();

  let mut lockfile = Lockfile::new_empty(deno_lock_path.to_path_buf(), false);
  for (path, package) in &packages {
    let dependencies = package
      .dependencies
      .iter()
      .filter(|(_, specifier)| !is_non_registry_specifier(specifier))
      .filter_map(|(name, _)| {
        dependency_lockfile_info(path, name, &package_lock.packages, &ids)
      })
      .collect();
    let optional_dependencies = package
      .optional_dependencies
      .iter()
      .filter(|(_, specifier)| !is_non_registry_specifier(specifier))
      .filter_map(|(name, _)| {
        dependency_lockfile_info(path, name, &package_lock.packages, &ids)
      })
      .collect();
    let optional_peers = package
      .peer_dependencies
      .iter()
      .filter(|(name, _)| {
        package
          .peer_dependencies_meta
          .get(*name)
          .is_some_and(|meta| meta.optional)
      })
      .filter(|(_, specifier)| !is_non_registry_specifier(specifier))
      .filter_map(|(name, _)| {
        dependency_lockfile_info(path, name, &package_lock.packages, &ids)
      })
      .collect();
    let peer_dependencies = package
      .peer_dependencies
      .iter()
      .filter(|(name, _)| {
        !package
          .peer_dependencies_meta
          .get(*name)
          .is_some_and(|meta| meta.optional)
      })
      .filter(|(_, specifier)| !is_non_registry_specifier(specifier))
      .filter_map(|(name, _)| {
        dependency_lockfile_info(path, name, &package_lock.packages, &ids)
      })
      .collect::<Vec<_>>();

    lockfile.insert_npm_package(NpmPackageLockfileInfo {
      serialized_id: StackString::from_string(ids[path].clone()),
      integrity: Some(package.integrity.clone()),
      dependencies: dependencies.into_iter().chain(peer_dependencies).collect(),
      optional_dependencies,
      optional_peers,
      os: package
        .os
        .iter()
        .map(|value| SmallStackString::from_string(value.clone()))
        .collect(),
      cpu: package
        .cpu
        .iter()
        .map(|value| SmallStackString::from_string(value.clone()))
        .collect(),
      tarball: package
        .resolved
        .as_ref()
        .map(|value| StackString::from_string(value.clone())),
      deprecated: false,
      scripts: false,
      bin: false,
    });
  }

  if let Some(root) = package_lock.packages.get("") {
    for (name, specifier) in root
      .dependencies
      .iter()
      .chain(&root.optional_dependencies)
      .chain(&root.peer_dependencies)
      .chain(&root.dev_dependencies)
    {
      if is_non_registry_specifier(specifier) {
        continue;
      }
      let Some(path) =
        resolve_dependency_path("", name, &package_lock.packages)
      else {
        continue;
      };
      let Some(package) = packages.get(&path) else {
        continue;
      };
      let Ok(version_req) = VersionReq::parse_from_npm(specifier) else {
        continue;
      };
      if package.name.as_str() != name {
        continue;
      }
      let id = &ids[&path];
      let Some(version) = id.strip_prefix(&format!("{}@", package.name)) else {
        continue;
      };
      lockfile.content.packages.specifiers.insert(
        JsrDepPackageReq::npm(PackageReq {
          name: package.name.clone(),
          version_req,
        }),
        SmallStackString::from_string(version.to_string()),
      );
    }
  }

  Some(lockfile.as_json_string())
}

fn is_non_registry_specifier(specifier: &str) -> bool {
  let specifier = specifier.trim();
  specifier.starts_with("file:")
    || specifier.starts_with("git:")
    || specifier.starts_with("git+")
    || specifier.starts_with("github:")
    || specifier.starts_with("gitlab:")
    || specifier.starts_with("bitbucket:")
    || specifier.starts_with("link:")
    || specifier.starts_with("workspace:")
    || specifier.starts_with("http:")
    || specifier.starts_with("https:")
    || specifier.starts_with("ssh:")
    || specifier.contains("://")
}

fn package_name_from_path(path: &str) -> Option<&str> {
  let (_, name) = path.rsplit_once("node_modules/")?;
  (!name.is_empty()).then_some(name)
}

fn resolve_dependency_path(
  package_path: &str,
  dependency_name: &str,
  packages: &BTreeMap<String, PackageLockPackage>,
) -> Option<String> {
  let mut current = package_path;
  loop {
    let candidate = if current.is_empty() {
      format!("node_modules/{dependency_name}")
    } else {
      format!("{current}/node_modules/{dependency_name}")
    };
    if packages.contains_key(&candidate) {
      return Some(candidate);
    }
    current = if let Some((parent, _)) = current.rsplit_once("/node_modules/") {
      parent
    } else if current.starts_with("node_modules/") {
      ""
    } else {
      return None;
    };
  }
}

fn resolve_dependency_path_in_packages(
  package_path: &str,
  dependency_name: &str,
  packages: &BTreeMap<String, Package>,
) -> Option<String> {
  let mut current = package_path;
  loop {
    let candidate = if current.is_empty() {
      format!("node_modules/{dependency_name}")
    } else {
      format!("{current}/node_modules/{dependency_name}")
    };
    if packages.contains_key(&candidate) {
      return Some(candidate);
    }
    current = if let Some((parent, _)) = current.rsplit_once("/node_modules/") {
      parent
    } else if current.starts_with("node_modules/") {
      ""
    } else {
      return None;
    };
  }
}

fn dependency_lockfile_info(
  package_path: &str,
  dependency_name: &str,
  package_lock_packages: &BTreeMap<String, PackageLockPackage>,
  ids: &HashMap<String, String>,
) -> Option<NpmPackageDependencyLockfileInfo> {
  let dependency_path = resolve_dependency_path(
    package_path,
    dependency_name,
    package_lock_packages,
  )?;
  Some(NpmPackageDependencyLockfileInfo {
    name: StackString::from_string(dependency_name.to_string()),
    id: StackString::from_string(ids.get(&dependency_path)?.clone()),
  })
}

fn package_id(
  path: &str,
  level: usize,
  packages: &BTreeMap<String, Package>,
  visiting: &mut HashSet<String>,
) -> String {
  let package = &packages[path];
  let name = if level == 0 {
    package.name.to_string()
  } else {
    package.name.replace('/', "+")
  };
  let mut id = format!("{name}@{}", package.version);
  if !visiting.insert(path.to_string()) {
    return id;
  }
  for (peer_name, peer_specifier) in &package.peer_dependencies {
    if package
      .peer_dependencies_meta
      .get(peer_name)
      .is_some_and(|meta| meta.optional)
      || is_non_registry_specifier(peer_specifier)
    {
      continue;
    }
    if let Some(peer_path) =
      resolve_dependency_path_in_packages(path, peer_name, packages)
    {
      id.push_str(&"_".repeat(level + 1));
      id.push_str(&package_id(&peer_path, level + 1, packages, visiting));
    }
  }
  visiting.remove(path);
  id
}

#[cfg(test)]
mod test {
  use std::path::Path;

  use super::package_lock_to_deno_lockfile;

  #[test]
  fn translates_nested_scoped_optional_and_peer_dependencies() {
    let lockfile = package_lock_to_deno_lockfile(
      Path::new("/project/deno.lock"),
      r#"{
        "lockfileVersion": 3,
        "packages": {
          "": {
            "name": "project",
            "dependencies": {
              "@scope/outer": "^1.0.0",
              "skipped": "file:../skipped"
            },
            "devDependencies": {
              "dev": "^1.0.0"
            }
          },
          "node_modules/@scope/outer": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/@scope/outer/-/outer-1.0.0.tgz",
            "integrity": "sha512-outer",
            "dependencies": {
              "inner": "^2.0.0"
            },
            "optionalDependencies": {
              "optional": "^1.0.0"
            },
            "peerDependencies": {
              "peer": "^1.0.0"
            },
            "os": ["linux"],
            "cpu": ["x64"]
          },
          "node_modules/@scope/outer/node_modules/inner": {
            "version": "2.0.0",
            "resolved": "https://registry.npmjs.org/inner/-/inner-2.0.0.tgz",
            "integrity": "sha512-inner"
          },
          "node_modules/optional": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/optional/-/optional-1.0.0.tgz",
            "integrity": "sha512-optional"
          },
          "node_modules/peer": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/peer/-/peer-1.0.0.tgz",
            "integrity": "sha512-peer"
          },
          "node_modules/dev": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/dev/-/dev-1.0.0.tgz",
            "integrity": "sha512-dev"
          },
          "node_modules/skipped": {
            "version": "1.0.0",
            "resolved": "file:../skipped",
            "integrity": "sha512-skipped"
          }
        }
      }"#,
    )
    .unwrap();

    assert!(
      lockfile.contains(r#""npm:@scope/outer@^1.0.0": "1.0.0_peer@1.0.0""#)
    );
    assert!(lockfile.contains(r#""npm:dev@^1.0.0": "1.0.0""#));
    assert!(lockfile.contains(r#""@scope/outer@1.0.0_peer@1.0.0""#));
    assert!(lockfile.contains(r#""dependencies": ["inner", "peer"]"#));
    assert!(lockfile.contains(r#""optionalDependencies": ["optional"]"#));
    assert!(lockfile.contains(r#""os": ["linux"]"#));
    assert!(lockfile.contains(r#""cpu": ["x64"]"#));
    assert!(!lockfile.contains("skipped"));
  }

  #[test]
  fn ignores_invalid_or_unsupported_package_locks() {
    assert!(
      package_lock_to_deno_lockfile(Path::new("deno.lock"), "not json")
        .is_none()
    );
    assert!(
      package_lock_to_deno_lockfile(
        Path::new("deno.lock"),
        r#"{"lockfileVersion": 1}"#,
      )
      .is_none()
    );
  }
}
