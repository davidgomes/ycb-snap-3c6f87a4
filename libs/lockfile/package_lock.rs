// Copyright 2018-2026 the Deno authors. MIT license.

//! Translation of npm's `package-lock.json` (lockfileVersion 2 and 3) into a
//! deno lockfile.
//!
//! This is used to seed a `deno.lock` from an existing npm lockfile the first
//! time `deno install` runs in a project migrating from npm, so the versions
//! and integrity hashes already pinned by npm are preserved instead of being
//! re-resolved from scratch.
//!
//! Only packages that come from an npm registry are translated. Workspace
//! links, `file:`, `git:` and plain tarball URL dependencies (and any package
//! without an integrity hash) are skipped, along with any package that
//! requires them, so that the resulting lockfile always describes a closed
//! dependency graph.

use std::collections::BTreeMap;
use std::collections::HashSet;
use std::path::PathBuf;

use deno_semver::SmallStackString;
use deno_semver::StackString;
use deno_semver::Version;
use deno_semver::VersionReq;
use deno_semver::jsr::JsrDepPackageReq;
use deno_semver::package::PackageReq;
use serde::Deserialize;

use crate::Lockfile;
use crate::NpmPackageDependencyLockfileInfo;
use crate::NpmPackageLockfileInfo;

#[derive(Debug, thiserror::Error)]
pub enum PackageLockTranslationError {
  #[error("failed deserializing package-lock.json")]
  Deserialization(#[source] serde_json::Error),
  #[error("unsupported package-lock.json lockfile version {version}")]
  UnsupportedVersion { version: u64 },
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct NpmPackageLock {
  #[serde(default)]
  lockfile_version: u64,
  #[serde(default)]
  packages: BTreeMap<String, NpmPackageLockEntry>,
}

#[derive(Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
struct NpmPackageLockEntry {
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
  peer_dependencies_meta: BTreeMap<String, NpmPeerDependencyMeta>,
  #[serde(default)]
  os: Vec<String>,
  #[serde(default)]
  cpu: Vec<String>,
  #[serde(default)]
  has_install_script: bool,
  bin: Option<serde_json::Value>,
  deprecated: Option<serde_json::Value>,
}

impl NpmPackageLockEntry {
  fn is_optional_peer(&self, name: &str) -> bool {
    self
      .peer_dependencies_meta
      .get(name)
      .map(|m| m.optional)
      .unwrap_or(false)
  }
}

#[derive(Debug, Default, Deserialize)]
struct NpmPeerDependencyMeta {
  #[serde(default)]
  optional: bool,
}

/// A package installed in some `node_modules` directory that can be
/// represented in a deno lockfile.
#[derive(Debug)]
struct RegistryPackage<'a> {
  /// The real package name (respecting aliased installs).
  name: &'a str,
  version: Version,
  entry: &'a NpmPackageLockEntry,
}

impl RegistryPackage<'_> {
  fn serialized_id(&self) -> StackString {
    let mut id = StackString::with_capacity(
      self.name.len() + 1 + self.version.to_string().len(),
    );
    id.push_str(self.name);
    id.push('@');
    id.push_str(&self.version.to_string());
    id
  }
}

impl Lockfile {
  /// Creates a new lockfile seeded from the contents of an npm
  /// `package-lock.json` file (lockfileVersion 2 or 3).
  ///
  /// The resulting lockfile will have `has_content_changed` set when any
  /// packages were translated so that it gets written to the disk and the
  /// npm resolution is validated against the registry.
  pub fn from_npm_package_lock_json(
    file_path: PathBuf,
    package_lock_text: &str,
  ) -> Result<Lockfile, PackageLockTranslationError> {
    let package_lock: NpmPackageLock =
      serde_json::from_str(package_lock_text)
        .map_err(PackageLockTranslationError::Deserialization)?;
    if package_lock.lockfile_version != 2 && package_lock.lockfile_version != 3
    {
      return Err(PackageLockTranslationError::UnsupportedVersion {
        version: package_lock.lockfile_version,
      });
    }

    let mut lockfile = Lockfile::new_empty(file_path, false);
    let packages = &package_lock.packages;

    // collect the installed packages that can be represented in a deno
    // lockfile
    let mut candidates: BTreeMap<&str, RegistryPackage> = packages
      .iter()
      .filter_map(|(path, entry)| {
        let (_, install_name) = split_last_node_modules(path)?;
        if !is_valid_package_name(install_name) {
          return None;
        }
        if entry.link || !is_registry_entry(entry) {
          return None;
        }
        let version = Version::parse_from_npm(entry.version.as_ref()?).ok()?;
        Some((
          path.as_str(),
          RegistryPackage {
            name: entry.name.as_deref().unwrap_or(install_name),
            version,
            entry,
          },
        ))
      })
      .collect();

    // remove any package whose required dependencies can't be translated
    // (ex. a git dependency) so the emitted graph is always closed—repeat
    // because a removal may invalidate other packages
    loop {
      let removed_paths = candidates
        .iter()
        .filter_map(|(path, pkg)| {
          let has_untranslatable_dep =
            pkg.entry.dependencies.keys().any(|dep_name| {
              // a dependency that also appears in optionalDependencies
              // is optional and may be missing
              if pkg.entry.optional_dependencies.contains_key(dep_name) {
                return false;
              }
              !matches!(
                resolve_node_modules_dep(packages, path, dep_name),
                Some((target_path, _)) if candidates.contains_key(target_path)
              )
            });
          has_untranslatable_dep.then_some(*path)
        })
        .collect::<Vec<_>>();
      if removed_paths.is_empty() {
        break;
      }
      for path in removed_paths {
        candidates.remove(path);
      }
    }

    // emit the packages deduplicating by name@version (npm may install the
    // same package version at multiple nested locations)
    let mut seen_ids = HashSet::new();
    for (path, pkg) in &candidates {
      let id = pkg.serialized_id();
      if !seen_ids.insert(id.clone()) {
        continue;
      }
      let resolve_included_dep = |dep_name: &str| {
        let (target_path, _) =
          resolve_node_modules_dep(packages, path, dep_name)?;
        let target = candidates.get(target_path)?;
        Some(NpmPackageDependencyLockfileInfo {
          name: dep_name.into(),
          id: target.serialized_id(),
        })
      };
      let mut dependencies = Vec::new();
      for dep_name in pkg.entry.dependencies.keys() {
        if pkg.entry.optional_dependencies.contains_key(dep_name) {
          continue;
        }
        // guaranteed to resolve by the loop above
        dependencies.extend(resolve_included_dep(dep_name));
      }
      // non-optional peer dependencies that npm installed are part of the
      // resolved graph, so represent them as regular dependencies
      for dep_name in pkg.entry.peer_dependencies.keys() {
        if pkg.entry.is_optional_peer(dep_name)
          || pkg.entry.dependencies.contains_key(dep_name)
        {
          continue;
        }
        dependencies.extend(resolve_included_dep(dep_name));
      }
      let optional_dependencies = pkg
        .entry
        .optional_dependencies
        .keys()
        .filter_map(|dep_name| resolve_included_dep(dep_name))
        .collect();
      let optional_peers = pkg
        .entry
        .peer_dependencies
        .keys()
        .filter(|dep_name| pkg.entry.is_optional_peer(dep_name))
        .filter_map(|dep_name| resolve_included_dep(dep_name))
        .collect();
      lockfile.insert_npm_package(NpmPackageLockfileInfo {
        integrity: pkg.entry.integrity.clone(),
        dependencies,
        optional_dependencies,
        optional_peers,
        os: pkg.entry.os.iter().map(|s| s.as_str().into()).collect(),
        cpu: pkg.entry.cpu.iter().map(|s| s.as_str().into()).collect(),
        tarball: pkg.entry.resolved.as_ref().and_then(|resolved| {
          if *resolved == default_tarball_url(pkg.name, &pkg.version) {
            None
          } else {
            Some(resolved.as_str().into())
          }
        }),
        deprecated: match &pkg.entry.deprecated {
          Some(serde_json::Value::String(_)) => true,
          Some(serde_json::Value::Bool(value)) => *value,
          _ => false,
        },
        scripts: pkg.entry.has_install_script,
        bin: pkg
          .entry
          .bin
          .as_ref()
          .is_some_and(|v| !matches!(v, serde_json::Value::Null)),
        serialized_id: id,
      });
    }

    // seed the specifiers from the direct dependencies of the root package
    // and any workspace members
    for (importer_path, importer) in packages.iter().filter(|(path, entry)| {
      !entry.link
        && (path.is_empty() || split_last_node_modules(path).is_none())
    }) {
      for (alias, req_text) in importer
        .dependencies
        .iter()
        .chain(importer.dev_dependencies.iter())
      {
        let Some(package_req) = parse_package_json_dep_req(alias, req_text)
        else {
          continue;
        };
        let Some((target_path, _)) =
          resolve_node_modules_dep(packages, importer_path, alias)
        else {
          continue;
        };
        let Some(target) = candidates.get(target_path) else {
          continue;
        };
        if target.name != package_req.name.as_str() {
          continue;
        }
        // skip pinned versions that no longer satisfy the version
        // requirement in the package.json (ex. an out of date npm lockfile)
        if package_req.version_req.tag().is_none()
          && !package_req.version_req.matches(&target.version)
        {
          continue;
        }
        let version = SmallStackString::from_str(&target.version.to_string());
        lockfile
          .insert_package_specifier(JsrDepPackageReq::npm(package_req), version);
      }
    }

    Ok(lockfile)
  }
}

fn is_valid_package_name(name: &str) -> bool {
  match name.strip_prefix('@') {
    Some(rest) => {
      let mut parts = rest.split('/');
      matches!(
        (parts.next(), parts.next(), parts.next()),
        (Some(scope), Some(name), None) if !scope.is_empty() && !name.is_empty()
      )
    }
    None => !name.is_empty() && !name.contains('/'),
  }
}

fn is_registry_entry(entry: &NpmPackageLockEntry) -> bool {
  if !entry.integrity.as_deref().is_some_and(|i| !i.is_empty()) {
    return false;
  }
  match &entry.resolved {
    // registry tarballs conventionally live at `<registry>/<name>/-/<file>`,
    // so anything else (git, file and plain tarball url dependencies) is
    // considered to not come from an npm registry
    Some(resolved) => resolved
      .strip_prefix("https://")
      .or_else(|| resolved.strip_prefix("http://"))
      .is_some_and(|rest| rest.contains("/-/")),
    // no resolved url means the package came from the default registry
    None => true,
  }
}

/// Splits an installed package path into the path of the directory it was
/// installed in and the name it was installed under (ex.
/// `node_modules/foo/node_modules/@scope/bar` -> `node_modules/foo` and
/// `@scope/bar`). Returns `None` when the path is not in a `node_modules`
/// directory (ex. the root project or a workspace member).
fn split_last_node_modules(path: &str) -> Option<(&str, &str)> {
  const SEGMENT: &str = "node_modules/";
  let mut last_boundary_index = None;
  for (index, _) in path.match_indices(SEGMENT) {
    if index == 0 || path.as_bytes()[index - 1] == b'/' {
      last_boundary_index = Some(index);
    }
  }
  let index = last_boundary_index?;
  let name = &path[index + SEGMENT.len()..];
  if name.is_empty() || name.ends_with('/') {
    return None;
  }
  let parent = if index == 0 {
    ""
  } else {
    path[..index].trim_end_matches('/')
  };
  Some((parent, name))
}

/// Resolves a dependency name from a package directory the way node does:
/// first looking in the package's own `node_modules` directory and then
/// walking up the containing packages to the root of the project.
fn resolve_node_modules_dep<'a>(
  packages: &'a BTreeMap<String, NpmPackageLockEntry>,
  from_dir: &str,
  dep_name: &str,
) -> Option<(&'a str, &'a NpmPackageLockEntry)> {
  let mut dir = from_dir;
  loop {
    let candidate = if dir.is_empty() {
      format!("node_modules/{}", dep_name)
    } else {
      format!("{}/node_modules/{}", dir, dep_name)
    };
    if let Some((path, entry)) = packages.get_key_value(&candidate) {
      return Some((path.as_str(), entry));
    }
    if dir.is_empty() {
      return None;
    }
    dir = match dir.rfind("/node_modules/") {
      Some(index) => &dir[..index],
      None => "",
    };
  }
}

/// Parses a package.json dependency entry into a `PackageReq` the same way
/// deno interprets it (handling `npm:` aliases). Returns `None` for
/// dependencies that don't refer to an npm registry package (ex. `file:`,
/// `git:`, url or `workspace:` dependencies).
fn parse_package_json_dep_req(
  alias: &str,
  req_text: &str,
) -> Option<PackageReq> {
  fn parse_version_req(text: &str) -> Option<VersionReq> {
    if text.is_empty() {
      VersionReq::parse_from_npm("*").ok()
    } else {
      VersionReq::parse_from_npm(text).ok()
    }
  }

  match req_text.strip_prefix("npm:") {
    Some(rest) => {
      // aliased dependency (ex. `"foo": "npm:@scope/bar@^1"`)
      match rest.rfind('@') {
        Some(index) if index > 0 => Some(PackageReq {
          name: rest[..index].into(),
          version_req: parse_version_req(&rest[index + 1..])?,
        }),
        _ => Some(PackageReq {
          name: rest.into(),
          version_req: parse_version_req("")?,
        }),
      }
    }
    None => Some(PackageReq {
      name: alias.into(),
      version_req: parse_version_req(req_text)?,
    }),
  }
}

fn default_tarball_url(name: &str, version: &Version) -> String {
  let name_without_scope = match name.rsplit_once('/') {
    Some((_, name)) => name,
    None => name,
  };
  format!(
    "https://registry.npmjs.org/{}/-/{}-{}.tgz",
    name, name_without_scope, version
  )
}

#[cfg(test)]
mod tests {
  use std::path::PathBuf;

  use super::*;
  use crate::NpmPackageInfo;

  fn translate(text: &str) -> Lockfile {
    Lockfile::from_npm_package_lock_json(PathBuf::from("/deno.lock"), text)
      .unwrap()
  }

  fn npm_package<'a>(
    lockfile: &'a Lockfile,
    id: &str,
  ) -> &'a NpmPackageInfo {
    lockfile
      .content
      .packages
      .npm
      .get(id)
      .unwrap_or_else(|| panic!("package '{}' not found", id))
  }

  fn specifier(lockfile: &Lockfile, req: &str) -> Option<String> {
    lockfile
      .content
      .packages
      .specifiers
      .get(&JsrDepPackageReq::from_str(req).unwrap())
      .map(|v| v.to_string())
  }

  #[test]
  fn basic_translation() {
    let lockfile = translate(
      r#"{
      "name": "my-project",
      "lockfileVersion": 3,
      "packages": {
        "": {
          "name": "my-project",
          "dependencies": { "a": "^1.0.0" },
          "devDependencies": { "b": "~2.1.0" }
        },
        "node_modules/a": {
          "version": "1.0.2",
          "resolved": "https://registry.npmjs.org/a/-/a-1.0.2.tgz",
          "integrity": "sha512-aaa",
          "dependencies": { "c": "^3.0.0" }
        },
        "node_modules/b": {
          "version": "2.1.5",
          "resolved": "https://registry.npmjs.org/b/-/b-2.1.5.tgz",
          "integrity": "sha512-bbb",
          "dev": true
        },
        "node_modules/c": {
          "version": "3.2.0",
          "resolved": "https://registry.npmjs.org/c/-/c-3.2.0.tgz",
          "integrity": "sha512-ccc"
        }
      }
    }"#,
    );
    assert!(lockfile.has_content_changed);
    assert_eq!(specifier(&lockfile, "npm:a@^1.0.0"), Some("1.0.2".into()));
    assert_eq!(specifier(&lockfile, "npm:b@~2.1.0"), Some("2.1.5".into()));
    let a = npm_package(&lockfile, "a@1.0.2");
    assert_eq!(a.integrity.as_deref(), Some("sha512-aaa"));
    // the default registry tarball url is not stored
    assert_eq!(a.tarball, None);
    assert_eq!(
      a.dependencies.get("c").map(|s| s.as_str()),
      Some("c@3.2.0")
    );
    assert_eq!(
      npm_package(&lockfile, "b@2.1.5").integrity.as_deref(),
      Some("sha512-bbb")
    );
    assert_eq!(lockfile.content.packages.npm.len(), 3);
  }

  #[test]
  fn scoped_packages_and_aliases() {
    let lockfile = translate(
      r#"{
      "lockfileVersion": 2,
      "packages": {
        "": {
          "dependencies": {
            "@scope/a": "^1.0.0",
            "b-alias": "npm:@scope/b@^2.0.0"
          }
        },
        "node_modules/@scope/a": {
          "version": "1.1.0",
          "resolved": "https://registry.npmjs.org/@scope/a/-/a-1.1.0.tgz",
          "integrity": "sha512-scopea"
        },
        "node_modules/b-alias": {
          "name": "@scope/b",
          "version": "2.3.0",
          "resolved": "https://registry.npmjs.org/@scope/b/-/b-2.3.0.tgz",
          "integrity": "sha512-scopeb"
        }
      }
    }"#,
    );
    assert_eq!(
      specifier(&lockfile, "npm:@scope/a@^1.0.0"),
      Some("1.1.0".into())
    );
    assert_eq!(
      specifier(&lockfile, "npm:@scope/b@^2.0.0"),
      Some("2.3.0".into())
    );
    assert!(lockfile.content.packages.npm.contains_key("@scope/a@1.1.0"));
    assert!(lockfile.content.packages.npm.contains_key("@scope/b@2.3.0"));
  }

  #[test]
  fn nested_node_modules_resolution() {
    let lockfile = translate(
      r#"{
      "lockfileVersion": 3,
      "packages": {
        "": {
          "dependencies": { "a": "^1.0.0", "b": "^2.0.0" }
        },
        "node_modules/a": {
          "version": "1.0.0",
          "resolved": "https://registry.npmjs.org/a/-/a-1.0.0.tgz",
          "integrity": "sha512-a",
          "dependencies": { "b": "^1.0.0" }
        },
        "node_modules/a/node_modules/b": {
          "version": "1.5.0",
          "resolved": "https://registry.npmjs.org/b/-/b-1.5.0.tgz",
          "integrity": "sha512-b15"
        },
        "node_modules/b": {
          "version": "2.0.0",
          "resolved": "https://registry.npmjs.org/b/-/b-2.0.0.tgz",
          "integrity": "sha512-b2"
        }
      }
    }"#,
    );
    // `a` resolves its own nested copy of `b`
    assert_eq!(
      npm_package(&lockfile, "a@1.0.0")
        .dependencies
        .get("b")
        .map(|s| s.as_str()),
      Some("b@1.5.0")
    );
    // the root resolves the hoisted copy
    assert_eq!(specifier(&lockfile, "npm:b@^2.0.0"), Some("2.0.0".into()));
    assert!(lockfile.content.packages.npm.contains_key("b@1.5.0"));
    assert!(lockfile.content.packages.npm.contains_key("b@2.0.0"));
  }

  #[test]
  fn optional_and_peer_dependencies() {
    let lockfile = translate(
      r#"{
      "lockfileVersion": 3,
      "packages": {
        "": {
          "dependencies": { "a": "^1.0.0", "peer": "^1.0.0" }
        },
        "node_modules/a": {
          "version": "1.0.0",
          "resolved": "https://registry.npmjs.org/a/-/a-1.0.0.tgz",
          "integrity": "sha512-a",
          "optionalDependencies": {
            "fsevents": "^2.0.0",
            "missing-optional": "^1.0.0"
          },
          "peerDependencies": {
            "peer": "^1.0.0",
            "optional-peer": "^1.0.0",
            "missing-optional-peer": "^1.0.0"
          },
          "peerDependenciesMeta": {
            "optional-peer": { "optional": true },
            "missing-optional-peer": { "optional": true }
          }
        },
        "node_modules/fsevents": {
          "version": "2.3.2",
          "resolved": "https://registry.npmjs.org/fsevents/-/fsevents-2.3.2.tgz",
          "integrity": "sha512-fsevents",
          "hasInstallScript": true,
          "optional": true,
          "os": ["darwin"],
          "cpu": ["x64", "arm64"]
        },
        "node_modules/peer": {
          "version": "1.2.0",
          "resolved": "https://registry.npmjs.org/peer/-/peer-1.2.0.tgz",
          "integrity": "sha512-peer"
        },
        "node_modules/optional-peer": {
          "version": "1.3.0",
          "resolved": "https://registry.npmjs.org/optional-peer/-/optional-peer-1.3.0.tgz",
          "integrity": "sha512-optionalpeer"
        }
      }
    }"#,
    );
    let a = npm_package(&lockfile, "a@1.0.0");
    assert_eq!(
      a.optional_dependencies.get("fsevents").map(|s| s.as_str()),
      Some("fsevents@2.3.2")
    );
    // a missing optional dependency is simply not included
    assert!(!a.optional_dependencies.contains_key("missing-optional"));
    // a resolved non-optional peer is represented as a dependency
    assert_eq!(
      a.dependencies.get("peer").map(|s| s.as_str()),
      Some("peer@1.2.0")
    );
    assert_eq!(
      a.optional_peers.get("optional-peer").map(|s| s.as_str()),
      Some("optional-peer@1.3.0")
    );
    assert!(!a.optional_peers.contains_key("missing-optional-peer"));
    let fsevents = npm_package(&lockfile, "fsevents@2.3.2");
    assert_eq!(fsevents.os, vec![SmallStackString::from_str("darwin")]);
    assert_eq!(
      fsevents.cpu,
      vec![
        SmallStackString::from_str("x64"),
        SmallStackString::from_str("arm64")
      ]
    );
    assert!(fsevents.scripts);
  }

  #[test]
  fn skips_non_registry_packages() {
    let lockfile = translate(
      r#"{
      "lockfileVersion": 3,
      "packages": {
        "": {
          "dependencies": {
            "a": "^1.0.0",
            "git-dep": "github:user/repo",
            "file-dep": "file:../file-dep",
            "url-dep": "https://example.com/url-dep-1.0.0.tgz",
            "linked": "^1.0.0"
          }
        },
        "node_modules/a": {
          "version": "1.0.0",
          "resolved": "https://registry.npmjs.org/a/-/a-1.0.0.tgz",
          "integrity": "sha512-a"
        },
        "node_modules/git-dep": {
          "version": "1.0.0",
          "resolved": "git+ssh://git@github.com/user/repo.git#abc123"
        },
        "node_modules/file-dep": {
          "resolved": "../file-dep",
          "link": true
        },
        "node_modules/url-dep": {
          "version": "1.0.0",
          "resolved": "https://example.com/url-dep-1.0.0.tgz",
          "integrity": "sha512-urldep"
        },
        "node_modules/linked": {
          "resolved": "packages/linked",
          "link": true
        },
        "packages/linked": {
          "name": "linked",
          "version": "1.0.0"
        }
      }
    }"#,
    );
    assert_eq!(
      lockfile
        .content
        .packages
        .npm
        .keys()
        .map(|k| k.as_str())
        .collect::<Vec<_>>(),
      vec!["a@1.0.0"]
    );
    assert_eq!(
      lockfile
        .content
        .packages
        .specifiers
        .keys()
        .map(|k| k.to_string())
        .collect::<Vec<_>>(),
      vec!["npm:a@^1.0.0".to_string()]
    );
  }

  #[test]
  fn drops_packages_depending_on_non_registry_packages() {
    let lockfile = translate(
      r#"{
      "lockfileVersion": 3,
      "packages": {
        "": {
          "dependencies": { "a": "^1.0.0", "d": "^1.0.0" }
        },
        "node_modules/a": {
          "version": "1.0.0",
          "resolved": "https://registry.npmjs.org/a/-/a-1.0.0.tgz",
          "integrity": "sha512-a",
          "dependencies": { "b": "^1.0.0" }
        },
        "node_modules/b": {
          "version": "1.0.0",
          "resolved": "https://registry.npmjs.org/b/-/b-1.0.0.tgz",
          "integrity": "sha512-b",
          "dependencies": { "git-dep": "user/repo" }
        },
        "node_modules/git-dep": {
          "version": "1.0.0",
          "resolved": "git+ssh://git@github.com/user/repo.git#abc123"
        },
        "node_modules/d": {
          "version": "1.0.0",
          "resolved": "https://registry.npmjs.org/d/-/d-1.0.0.tgz",
          "integrity": "sha512-d"
        }
      }
    }"#,
    );
    // `b` requires an untranslatable git dependency and `a` requires `b`,
    // so both are dropped, but `d` remains
    assert_eq!(
      lockfile
        .content
        .packages
        .npm
        .keys()
        .map(|k| k.as_str())
        .collect::<Vec<_>>(),
      vec!["d@1.0.0"]
    );
    assert_eq!(specifier(&lockfile, "npm:a@^1.0.0"), None);
    assert_eq!(specifier(&lockfile, "npm:d@^1.0.0"), Some("1.0.0".into()));
  }

  #[test]
  fn skips_packages_without_integrity() {
    let lockfile = translate(
      r#"{
      "lockfileVersion": 3,
      "packages": {
        "": {
          "dependencies": { "a": "^1.0.0" }
        },
        "node_modules/a": {
          "version": "1.0.0",
          "resolved": "https://registry.npmjs.org/a/-/a-1.0.0.tgz"
        }
      }
    }"#,
    );
    assert!(lockfile.content.packages.npm.is_empty());
    assert!(lockfile.content.packages.specifiers.is_empty());
    assert!(!lockfile.has_content_changed);
  }

  #[test]
  fn workspace_members() {
    let lockfile = translate(
      r#"{
      "lockfileVersion": 3,
      "packages": {
        "": {
          "workspaces": ["packages/*"]
        },
        "node_modules/member": {
          "resolved": "packages/member",
          "link": true
        },
        "packages/member": {
          "name": "member",
          "version": "1.0.0",
          "dependencies": { "a": "^1.0.0" }
        },
        "packages/member/node_modules/a": {
          "version": "1.2.0",
          "resolved": "https://registry.npmjs.org/a/-/a-1.2.0.tgz",
          "integrity": "sha512-a12"
        }
      }
    }"#,
    );
    // the workspace member's dependency resolves through its own
    // node_modules directory
    assert_eq!(specifier(&lockfile, "npm:a@^1.0.0"), Some("1.2.0".into()));
    assert!(lockfile.content.packages.npm.contains_key("a@1.2.0"));
  }

  #[test]
  fn stale_pin_not_matching_req_is_skipped() {
    let lockfile = translate(
      r#"{
      "lockfileVersion": 3,
      "packages": {
        "": {
          "dependencies": { "a": "^2.0.0" }
        },
        "node_modules/a": {
          "version": "1.0.0",
          "resolved": "https://registry.npmjs.org/a/-/a-1.0.0.tgz",
          "integrity": "sha512-a"
        }
      }
    }"#,
    );
    assert_eq!(specifier(&lockfile, "npm:a@^2.0.0"), None);
  }

  #[test]
  fn custom_registry_tarball_is_preserved() {
    let lockfile = translate(
      r#"{
      "lockfileVersion": 3,
      "packages": {
        "": {
          "dependencies": { "a": "^1.0.0" }
        },
        "node_modules/a": {
          "version": "1.0.0",
          "resolved": "https://custom-registry.example.com/a/-/a-1.0.0.tgz",
          "integrity": "sha512-a"
        }
      }
    }"#,
    );
    assert_eq!(
      npm_package(&lockfile, "a@1.0.0").tarball.as_deref(),
      Some("https://custom-registry.example.com/a/-/a-1.0.0.tgz")
    );
  }

  #[test]
  fn unsupported_lockfile_versions() {
    for text in [
      r#"{ "lockfileVersion": 1, "dependencies": {} }"#,
      r#"{ "dependencies": {} }"#,
      r#"{ "lockfileVersion": 4 }"#,
    ] {
      assert!(matches!(
        Lockfile::from_npm_package_lock_json(
          PathBuf::from("/deno.lock"),
          text
        ),
        Err(PackageLockTranslationError::UnsupportedVersion { .. })
      ));
    }
    assert!(matches!(
      Lockfile::from_npm_package_lock_json(
        PathBuf::from("/deno.lock"),
        "not json"
      ),
      Err(PackageLockTranslationError::Deserialization(_))
    ));
  }
}
