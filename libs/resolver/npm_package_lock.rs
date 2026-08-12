// Copyright 2018-2026 the Deno authors. MIT license.

//! Translates an npm `package-lock.json` (lockfileVersion 2 or 3) into
//! deno lockfile package content. This is used to seed npm resolution on
//! the first local `deno install` in a project migrating from npm, so the
//! versions and integrity hashes already pinned by npm are preserved
//! instead of being re-resolved from scratch.

use std::collections::BTreeMap;
use std::collections::HashMap;

use deno_lockfile::NpmPackageInfo;
use deno_lockfile::PackagesContent;
use deno_npm::resolution::DefaultTarballUrlProvider;
use deno_npm::resolution::NpmRegistryDefaultTarballUrlProvider;
use deno_package_json::PackageJsonDepValue;
use deno_semver::SmallStackString;
use deno_semver::StackString;
use deno_semver::Version;
use deno_semver::jsr::JsrDepPackageReq;
use deno_semver::package::PackageNv;
use indexmap::IndexMap;
use serde::Deserialize;
use url::Url;

#[derive(Debug, thiserror::Error)]
pub enum NpmPackageLockTranslateError {
  #[error("failed deserializing")]
  Deserialize(#[from] serde_json::Error),
  #[error("unsupported lockfileVersion: {0} (expected 2 or 3)")]
  UnsupportedLockfileVersion(u64),
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
  /// Only set when it differs from the name derived from the path
  /// (ex. aliased packages).
  name: Option<String>,
  version: Option<String>,
  resolved: Option<String>,
  integrity: Option<String>,
  #[serde(default)]
  link: bool,
  #[serde(default)]
  has_install_script: bool,
  bin: Option<serde_json::Value>,
  #[serde(default)]
  os: Vec<SmallStackString>,
  #[serde(default)]
  cpu: Vec<SmallStackString>,
  #[serde(default)]
  dependencies: IndexMap<String, String>,
  #[serde(default)]
  dev_dependencies: IndexMap<String, String>,
  #[serde(default)]
  optional_dependencies: IndexMap<String, String>,
  #[serde(default)]
  peer_dependencies: IndexMap<String, String>,
  #[serde(default)]
  peer_dependencies_meta: IndexMap<String, serde_json::Value>,
}

impl NpmPackageLockEntry {
  fn is_optional_peer(&self, name: &str) -> bool {
    self
      .peer_dependencies_meta
      .get(name)
      .and_then(|meta| meta.get("optional"))
      .and_then(|optional| optional.as_bool())
      .unwrap_or(false)
  }
}

/// A package entry that could be translated to a deno lockfile entry.
struct ResolvedPackage<'a> {
  entry: &'a NpmPackageLockEntry,
  /// The real package name (which might differ from the name derived
  /// from the entry's path for aliased packages).
  name: &'a str,
  version: Version,
  /// Serialized `name@version`.
  id: StackString,
  /// How deeply nested in `node_modules` directories the entry is.
  depth: usize,
}

/// Translates the text of an npm `package-lock.json` (lockfileVersion 2
/// or 3) to deno lockfile package content.
///
/// Entries that can't be represented are skipped:
/// - workspace links and workspace member folders (the members' deps
///   still contribute to the resolved specifiers)
/// - packages not resolved from one of the provided npm registries
///   (ex. `file:`, `git+`, or plain remote tarball dependencies)
/// - packages without an integrity hash (ex. bundled dependencies)
///
/// Dependency edges pointing at skipped packages are dropped so that the
/// resulting content is self-consistent.
pub fn packages_content_from_npm_package_lock(
  text: &str,
  registry_urls: &[Url],
) -> Result<PackagesContent, NpmPackageLockTranslateError> {
  let lock: NpmPackageLock = serde_json::from_str(text)?;
  if lock.lockfile_version != 2 && lock.lockfile_version != 3 {
    return Err(NpmPackageLockTranslateError::UnsupportedLockfileVersion(
      lock.lockfile_version,
    ));
  }

  // collect the entries that can be translated to deno lockfile entries
  let mut kept: HashMap<&str, ResolvedPackage> =
    HashMap::with_capacity(lock.packages.len());
  for (path, entry) in &lock.packages {
    if entry.link {
      continue;
    }
    let Some(path_name) = package_name_from_path(path) else {
      // the root project or a workspace member folder
      continue;
    };
    let name = entry.name.as_deref().unwrap_or(path_name);
    let Some(version) = entry
      .version
      .as_deref()
      .and_then(|v| Version::parse_from_npm(v).ok())
    else {
      continue;
    };
    if entry.integrity.is_none() {
      continue;
    }
    // skip file:, git+, and other non-registry dependencies
    if let Some(resolved) = &entry.resolved
      && !is_registry_tarball_url(resolved, registry_urls)
    {
      continue;
    }
    let version_text = version.to_string();
    let mut id = StackString::with_capacity(name.len() + 1 + version_text.len());
    id.push_str(name);
    id.push('@');
    id.push_str(&version_text);
    kept.insert(
      path,
      ResolvedPackage {
        entry,
        name,
        version,
        id,
        depth: path.matches("node_modules/").count(),
      },
    );
  }

  // build the npm packages, preferring the least nested entry when the
  // same name and version appears in multiple places in the tree
  let default_tarball_url_provider = NpmRegistryDefaultTarballUrlProvider;
  let mut npm: BTreeMap<StackString, NpmPackageInfo> = Default::default();
  let mut chosen_depths: HashMap<StackString, usize> =
    HashMap::with_capacity(lock.packages.len());
  for path in lock.packages.keys() {
    let Some(pkg) = kept.get(path.as_str()) else {
      continue;
    };
    if let Some(chosen_depth) = chosen_depths.get(&pkg.id)
      && *chosen_depth <= pkg.depth
    {
      continue;
    }
    chosen_depths.insert(pkg.id.clone(), pkg.depth);

    let mut dependencies = BTreeMap::new();
    let mut optional_dependencies = BTreeMap::new();
    let mut optional_peers = BTreeMap::new();
    for alias in pkg.entry.dependencies.keys() {
      if let Some(dep) = resolve_dep(&kept, path, alias) {
        dependencies.insert(StackString::from(alias.as_str()), dep.id.clone());
      }
    }
    for alias in pkg.entry.peer_dependencies.keys() {
      // only peers that npm actually placed in the tree are resolved
      if let Some(dep) = resolve_dep(&kept, path, alias) {
        let is_optional_peer = pkg.entry.is_optional_peer(alias);
        let alias = StackString::from(alias.as_str());
        if is_optional_peer {
          optional_peers.insert(alias.clone(), dep.id.clone());
        }
        dependencies.insert(alias, dep.id.clone());
      }
    }
    for alias in pkg.entry.optional_dependencies.keys() {
      if let Some(dep) = resolve_dep(&kept, path, alias) {
        let alias = StackString::from(alias.as_str());
        dependencies.remove(&alias);
        optional_dependencies.insert(alias, dep.id.clone());
      }
    }

    npm.insert(
      pkg.id.clone(),
      NpmPackageInfo {
        integrity: pkg.entry.integrity.clone(),
        dependencies,
        optional_dependencies,
        optional_peers,
        os: pkg.entry.os.clone(),
        cpu: pkg.entry.cpu.clone(),
        tarball: pkg.entry.resolved.as_deref().and_then(|resolved| {
          let nv = PackageNv {
            name: pkg.name.into(),
            version: pkg.version.clone(),
          };
          if resolved == default_tarball_url_provider.default_tarball_url(&nv) {
            None
          } else {
            Some(StackString::from_str(resolved))
          }
        }),
        deprecated: false,
        scripts: pkg.entry.has_install_script,
        bin: pkg
          .entry
          .bin
          .as_ref()
          .map(|bin| !bin.is_null())
          .unwrap_or(false),
      },
    );
  }

  // resolve the specifiers of the root project and any workspace member
  // folders the same way deno collects package.json dep requirements
  // (dependencies and devDependencies only)
  let mut specifiers: HashMap<JsrDepPackageReq, SmallStackString> =
    HashMap::new();
  for (path, entry) in &lock.packages {
    let is_importer = path.is_empty()
      || (!entry.link && package_name_from_path(path).is_none());
    if !is_importer {
      continue;
    }
    for (alias, req_text) in entry
      .dependencies
      .iter()
      .chain(entry.dev_dependencies.iter())
    {
      let Ok(PackageJsonDepValue::Req(req)) =
        PackageJsonDepValue::parse(alias, req_text)
      else {
        continue;
      };
      let Some(dep) = resolve_dep(&kept, path, alias) else {
        continue;
      };
      if dep.name != req.name {
        continue;
      }
      let version: SmallStackString = dep.version.to_string().as_str().into();
      specifiers
        .entry(JsrDepPackageReq::npm(req))
        .or_insert(version);
    }
  }

  Ok(PackagesContent {
    specifiers,
    jsr: Default::default(),
    npm,
  })
}

/// Extracts the package name from a `packages` entry path
/// (ex. `node_modules/foo/node_modules/@scope/bar` -> `@scope/bar`).
/// Returns `None` for paths that aren't inside a `node_modules` folder
/// (the root project or workspace member folders).
fn package_name_from_path(path: &str) -> Option<&str> {
  const SEARCH: &str = "node_modules/";
  let index = path.rfind(SEARCH)?;
  if index > 0 && !path[..index].ends_with('/') {
    return None;
  }
  let name = &path[index + SEARCH.len()..];
  let mut parts = name.split('/');
  let first = parts.next()?;
  if first.is_empty() {
    return None;
  }
  if first.starts_with('@') {
    // a scoped name has exactly two parts
    let second = parts.next()?;
    if second.is_empty() || parts.next().is_some() {
      return None;
    }
  } else if parts.next().is_some() {
    return None;
  }
  Some(name)
}

fn is_registry_tarball_url(resolved: &str, registry_urls: &[Url]) -> bool {
  registry_urls.iter().any(|registry_url| {
    let registry_url = registry_url.as_str();
    let registry_url = registry_url.strip_suffix('/').unwrap_or(registry_url);
    resolved
      .strip_prefix(registry_url)
      .is_some_and(|remainder| remainder.starts_with('/'))
  })
}

/// Resolves the package a dependency name refers to for the package at
/// `base_path` by walking up the `node_modules` folders the same way
/// node's resolution algorithm does.
fn resolve_dep<'resolved, 'lock>(
  kept: &'resolved HashMap<&'lock str, ResolvedPackage<'lock>>,
  base_path: &str,
  dep_name: &str,
) -> Option<&'resolved ResolvedPackage<'lock>> {
  let mut base_path = base_path;
  loop {
    let candidate = if base_path.is_empty() {
      format!("node_modules/{}", dep_name)
    } else {
      format!("{}/node_modules/{}", base_path, dep_name)
    };
    if let Some(pkg) = kept.get(candidate.as_str()) {
      return Some(pkg);
    }
    if base_path.is_empty() {
      return None;
    }
    base_path = parent_scope_path(base_path);
  }
}

/// Moves a `packages` entry path up to the enclosing package or folder
/// (ex. `node_modules/a/node_modules/b` -> `node_modules/a` -> ``).
fn parent_scope_path(path: &str) -> &str {
  if let Some(index) = path.rfind("/node_modules/") {
    &path[..index]
  } else if path == "node_modules" || path.starts_with("node_modules/") {
    ""
  } else if let Some(index) = path.rfind('/') {
    &path[..index]
  } else {
    ""
  }
}

#[cfg(test)]
mod tests {
  use std::path::PathBuf;

  use super::*;

  fn translate(text: &str) -> Result<PackagesContent, NpmPackageLockTranslateError> {
    translate_with_registries(
      text,
      &[Url::parse("https://registry.npmjs.org/").unwrap()],
    )
  }

  fn translate_with_registries(
    text: &str,
    registry_urls: &[Url],
  ) -> Result<PackagesContent, NpmPackageLockTranslateError> {
    packages_content_from_npm_package_lock(text, registry_urls)
  }

  #[track_caller]
  fn assert_translates(
    text: &str,
    registry_urls: &[Url],
    expected_lockfile: &str,
  ) {
    let packages = translate_with_registries(text, registry_urls).unwrap();
    let mut lockfile =
      deno_lockfile::Lockfile::new_empty(PathBuf::from("deno.lock"), false);
    lockfile.content.packages = packages;
    assert_eq!(lockfile.as_json_string(), expected_lockfile);
  }

  fn npmjs_registry() -> Vec<Url> {
    vec![Url::parse("https://registry.npmjs.org/").unwrap()]
  }

  #[test]
  fn basic() {
    assert_translates(
      r#"{
  "name": "app",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": {
      "name": "app",
      "version": "1.0.0",
      "dependencies": {
        "chalk": "^5.0.0"
      },
      "devDependencies": {
        "picocolors": "~1.0.0"
      }
    },
    "node_modules/chalk": {
      "version": "5.3.0",
      "resolved": "https://registry.npmjs.org/chalk/-/chalk-5.3.0.tgz",
      "integrity": "sha512-chalk",
      "engines": {
        "node": ">=12"
      }
    },
    "node_modules/picocolors": {
      "version": "1.0.0",
      "resolved": "https://registry.npmjs.org/picocolors/-/picocolors-1.0.0.tgz",
      "integrity": "sha512-pico"
    }
  }
}"#,
      &npmjs_registry(),
      r#"{
  "version": "5",
  "specifiers": {
    "npm:chalk@5": "5.3.0",
    "npm:picocolors@1.0": "1.0.0"
  },
  "npm": {
    "chalk@5.3.0": {
      "integrity": "sha512-chalk"
    },
    "picocolors@1.0.0": {
      "integrity": "sha512-pico"
    }
  }
}
"#,
    );
  }

  #[test]
  fn scoped_aliased_and_nested_packages() {
    assert_translates(
      r#"{
  "lockfileVersion": 3,
  "packages": {
    "": {
      "dependencies": {
        "@scope/a": "^1.0.0",
        "b-alias": "npm:@scope/b@2",
        "c": "^1.0.0"
      }
    },
    "node_modules/@scope/a": {
      "version": "1.1.0",
      "resolved": "https://registry.npmjs.org/@scope/a/-/a-1.1.0.tgz",
      "integrity": "sha512-a",
      "dependencies": {
        "c": "^2.0.0",
        "c-alias": "npm:c@1"
      }
    },
    "node_modules/@scope/a/node_modules/c": {
      "version": "2.0.0",
      "resolved": "https://registry.npmjs.org/c/-/c-2.0.0.tgz",
      "integrity": "sha512-c2"
    },
    "node_modules/b-alias": {
      "name": "@scope/b",
      "version": "2.3.0",
      "resolved": "https://registry.npmjs.org/@scope/b/-/b-2.3.0.tgz",
      "integrity": "sha512-b"
    },
    "node_modules/c": {
      "version": "1.0.0",
      "resolved": "https://registry.npmjs.org/c/-/c-1.0.0.tgz",
      "integrity": "sha512-c1"
    },
    "node_modules/c-alias": {
      "name": "c",
      "version": "1.0.0",
      "resolved": "https://registry.npmjs.org/c/-/c-1.0.0.tgz",
      "integrity": "sha512-c1"
    }
  }
}"#,
      &npmjs_registry(),
      r#"{
  "version": "5",
  "specifiers": {
    "npm:@scope/a@1": "1.1.0",
    "npm:@scope/b@2": "2.3.0",
    "npm:c@1": "1.0.0"
  },
  "npm": {
    "@scope/a@1.1.0": {
      "integrity": "sha512-a",
      "dependencies": [
        "c@2.0.0",
        "c-alias@npm:c@1.0.0"
      ]
    },
    "@scope/b@2.3.0": {
      "integrity": "sha512-b"
    },
    "c@1.0.0": {
      "integrity": "sha512-c1"
    },
    "c@2.0.0": {
      "integrity": "sha512-c2"
    }
  }
}
"#,
    );
  }

  #[test]
  fn optional_deps_peers_os_cpu_scripts_and_bin() {
    assert_translates(
      r#"{
  "lockfileVersion": 3,
  "packages": {
    "": {
      "dependencies": {
        "a": "^1.0.0"
      }
    },
    "node_modules/a": {
      "version": "1.0.0",
      "resolved": "https://registry.npmjs.org/a/-/a-1.0.0.tgz",
      "integrity": "sha512-a",
      "hasInstallScript": true,
      "bin": {
        "a": "bin/a.js"
      },
      "optionalDependencies": {
        "b": "^1.0.0"
      },
      "peerDependencies": {
        "c": "^1.0.0",
        "d": "^1.0.0",
        "missing-peer": "^1.0.0"
      },
      "peerDependenciesMeta": {
        "d": {
          "optional": true
        },
        "missing-peer": {
          "optional": true
        }
      }
    },
    "node_modules/b": {
      "version": "1.0.0",
      "resolved": "https://registry.npmjs.org/b/-/b-1.0.0.tgz",
      "integrity": "sha512-b",
      "os": ["darwin", "linux"],
      "cpu": ["x64", "arm64"],
      "optional": true
    },
    "node_modules/c": {
      "version": "1.0.0",
      "resolved": "https://registry.npmjs.org/c/-/c-1.0.0.tgz",
      "integrity": "sha512-c"
    },
    "node_modules/d": {
      "version": "1.0.0",
      "resolved": "https://registry.npmjs.org/d/-/d-1.0.0.tgz",
      "integrity": "sha512-d"
    }
  }
}"#,
      &npmjs_registry(),
      r#"{
  "version": "5",
  "specifiers": {
    "npm:a@1": "1.0.0"
  },
  "npm": {
    "a@1.0.0": {
      "integrity": "sha512-a",
      "dependencies": [
        "c",
        "d"
      ],
      "optionalDependencies": [
        "b"
      ],
      "optionalPeers": [
        "d"
      ],
      "scripts": true,
      "bin": true
    },
    "b@1.0.0": {
      "integrity": "sha512-b",
      "os": ["darwin", "linux"],
      "cpu": ["x64", "arm64"]
    },
    "c@1.0.0": {
      "integrity": "sha512-c"
    },
    "d@1.0.0": {
      "integrity": "sha512-d"
    }
  }
}
"#,
    );
  }

  #[test]
  fn skips_non_registry_deps_and_packages_without_integrity() {
    // lockfileVersion 2 files additionally have a legacy v1 "dependencies"
    // section, which is ignored
    assert_translates(
      r#"{
  "lockfileVersion": 2,
  "packages": {
    "": {
      "dependencies": {
        "file-dep": "file:../file-dep",
        "git-dep": "git+https://github.com/user/repo.git",
        "kept": "^1.0.0",
        "linked": "^1.0.0",
        "no-integrity": "^1.0.0",
        "remote": "https://example.com/remote-1.0.0.tgz"
      }
    },
    "../file-dep": {
      "name": "file-dep",
      "version": "1.0.0"
    },
    "node_modules/file-dep": {
      "resolved": "../file-dep",
      "link": true
    },
    "node_modules/git-dep": {
      "version": "1.0.0",
      "resolved": "git+ssh://git@github.com/user/repo.git#abc123"
    },
    "node_modules/kept": {
      "version": "1.0.0",
      "resolved": "https://registry.npmjs.org/kept/-/kept-1.0.0.tgz",
      "integrity": "sha512-kept",
      "dependencies": {
        "git-dep": "git+https://github.com/user/repo.git",
        "no-integrity": "^1.0.0"
      }
    },
    "node_modules/linked": {
      "resolved": "workspaces/linked",
      "link": true
    },
    "workspaces/linked": {
      "name": "linked",
      "version": "1.0.0"
    },
    "node_modules/no-integrity": {
      "version": "1.0.0",
      "resolved": "https://registry.npmjs.org/no-integrity/-/no-integrity-1.0.0.tgz"
    },
    "node_modules/remote": {
      "version": "1.0.0",
      "resolved": "https://example.com/remote-1.0.0.tgz",
      "integrity": "sha512-remote"
    }
  },
  "dependencies": {
    "kept": {
      "version": "1.0.0",
      "resolved": "https://registry.npmjs.org/kept/-/kept-1.0.0.tgz",
      "integrity": "sha512-kept"
    }
  }
}"#,
      &npmjs_registry(),
      r#"{
  "version": "5",
  "specifiers": {
    "npm:kept@1": "1.0.0"
  },
  "npm": {
    "kept@1.0.0": {
      "integrity": "sha512-kept"
    }
  }
}
"#,
    );
  }

  #[test]
  fn workspace_member_deps() {
    assert_translates(
      r#"{
  "lockfileVersion": 3,
  "packages": {
    "": {
      "name": "root",
      "version": "1.0.0",
      "workspaces": ["packages/*"],
      "dependencies": {
        "a": "^1.0.0"
      }
    },
    "node_modules/a": {
      "version": "1.0.0",
      "resolved": "https://registry.npmjs.org/a/-/a-1.0.0.tgz",
      "integrity": "sha512-a"
    },
    "node_modules/b": {
      "version": "2.0.0",
      "resolved": "https://registry.npmjs.org/b/-/b-2.0.0.tgz",
      "integrity": "sha512-b"
    },
    "node_modules/member": {
      "resolved": "packages/member",
      "link": true
    },
    "packages/member": {
      "name": "member",
      "version": "1.0.0",
      "dependencies": {
        "b": "^2.0.0"
      },
      "devDependencies": {
        "c": "^1.0.0"
      }
    },
    "packages/member/node_modules/c": {
      "version": "1.0.0",
      "resolved": "https://registry.npmjs.org/c/-/c-1.0.0.tgz",
      "integrity": "sha512-c"
    }
  }
}"#,
      &npmjs_registry(),
      r#"{
  "version": "5",
  "specifiers": {
    "npm:a@1": "1.0.0",
    "npm:b@2": "2.0.0",
    "npm:c@1": "1.0.0"
  },
  "npm": {
    "a@1.0.0": {
      "integrity": "sha512-a"
    },
    "b@2.0.0": {
      "integrity": "sha512-b"
    },
    "c@1.0.0": {
      "integrity": "sha512-c"
    }
  }
}
"#,
    );
  }

  #[test]
  fn duplicate_name_and_version_prefers_least_nested() {
    assert_translates(
      r#"{
  "lockfileVersion": 3,
  "packages": {
    "": {
      "dependencies": {
        "a": "^1.0.0",
        "dup": "^1.0.0"
      }
    },
    "node_modules/a": {
      "version": "1.0.0",
      "resolved": "https://registry.npmjs.org/a/-/a-1.0.0.tgz",
      "integrity": "sha512-a",
      "dependencies": {
        "dup": "^1.0.0"
      }
    },
    "node_modules/a/node_modules/b": {
      "version": "1.0.0",
      "resolved": "https://registry.npmjs.org/b/-/b-1.0.0.tgz",
      "integrity": "sha512-b"
    },
    "node_modules/a/node_modules/dup": {
      "version": "1.0.0",
      "resolved": "https://registry.npmjs.org/dup/-/dup-1.0.0.tgz",
      "integrity": "sha512-dup",
      "dependencies": {
        "b": "^1.0.0"
      }
    },
    "node_modules/dup": {
      "version": "1.0.0",
      "resolved": "https://registry.npmjs.org/dup/-/dup-1.0.0.tgz",
      "integrity": "sha512-dup"
    }
  }
}"#,
      &npmjs_registry(),
      r#"{
  "version": "5",
  "specifiers": {
    "npm:a@1": "1.0.0",
    "npm:dup@1": "1.0.0"
  },
  "npm": {
    "a@1.0.0": {
      "integrity": "sha512-a",
      "dependencies": [
        "dup"
      ]
    },
    "b@1.0.0": {
      "integrity": "sha512-b"
    },
    "dup@1.0.0": {
      "integrity": "sha512-dup"
    }
  }
}
"#,
    );
  }

  #[test]
  fn custom_registry_records_tarball_url() {
    assert_translates(
      r#"{
  "lockfileVersion": 3,
  "packages": {
    "": {
      "dependencies": {
        "@denotest/esm-basic": "^1.0.0"
      }
    },
    "node_modules/@denotest/esm-basic": {
      "version": "1.0.0",
      "resolved": "http://localhost:4260/@denotest/esm-basic/1.0.0.tgz",
      "integrity": "sha512-basic"
    }
  }
}"#,
      &[Url::parse("http://localhost:4260/").unwrap()],
      r#"{
  "version": "5",
  "specifiers": {
    "npm:@denotest/esm-basic@1": "1.0.0"
  },
  "npm": {
    "@denotest/esm-basic@1.0.0": {
      "integrity": "sha512-basic",
      "tarball": "http://localhost:4260/@denotest/esm-basic/1.0.0.tgz"
    }
  }
}
"#,
    );
  }

  #[test]
  fn unsupported_lockfile_version() {
    let err = translate(r#"{ "lockfileVersion": 1, "dependencies": {} }"#)
      .unwrap_err();
    assert!(matches!(
      err,
      NpmPackageLockTranslateError::UnsupportedLockfileVersion(1)
    ));
    let err = translate(r#"{ "dependencies": {} }"#).unwrap_err();
    assert!(matches!(
      err,
      NpmPackageLockTranslateError::UnsupportedLockfileVersion(0)
    ));
  }

  #[test]
  fn invalid_json() {
    let err = translate("{ not valid json").unwrap_err();
    assert!(matches!(err, NpmPackageLockTranslateError::Deserialize(_)));
  }
}
