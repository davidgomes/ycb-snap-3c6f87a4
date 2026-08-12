// Copyright 2018-2026 the Deno authors. MIT license.

//! Seeds a freshly created Deno lockfile's npm section from a sibling npm
//! `package-lock.json` (lockfile version 2 or 3).
//!
//! Projects migrating from npm often have a `package-lock.json` but no
//! `deno.lock` yet. Without this, the first local `deno install` would start
//! npm resolution from scratch, which can pick different (newer) versions
//! than the ones already pinned by npm and drops their integrity hashes.
//! This module translates the npm lockfile into the equivalent
//! `deno_lockfile` npm section so that resolution reuses those pins instead.
//!
//! This is a best-effort translation used only to seed the very first
//! lockfile: it is never used when a `deno.lock` already exists, and it must
//! never cause `deno install` to fail. Anything that can't be translated with
//! confidence (workspace links, non-registry dependencies, packages missing
//! an integrity hash, unsupported lockfile versions, i/o or parse errors) is
//! simply skipped, falling back to an empty lockfile.

use std::collections::BTreeMap;
use std::collections::HashMap;
use std::path::Path;

use deno_lockfile::LockfileContent;
use deno_lockfile::NpmPackageInfo;
use deno_lockfile::PackagesContent;
use deno_package_json::PackageJsonDepValue;
use deno_semver::SmallStackString;
use deno_semver::StackString;
use deno_semver::Version;
use deno_semver::jsr::JsrDepPackageReq;
use node_resolver::PackageJson;
use serde_json::Map;
use serde_json::Value;

use crate::lockfile::LockfileSys;

const NPM_LOCKFILE_FILE_NAME: &str = "package-lock.json";
const PACKAGE_JSON_FILE_NAME: &str = "package.json";

/// A translated `package-lock.json` entry that's usable as a `deno_lockfile`
/// npm package (registry-resolved, with an integrity hash).
struct ValidEntry {
  name: StackString,
  version: StackString,
}

impl ValidEntry {
  fn id(&self) -> StackString {
    let mut id =
      StackString::with_capacity(self.name.len() + 1 + self.version.len());
    id.push_str(&self.name);
    id.push('@');
    id.push_str(&self.version);
    id
  }
}

/// Attempts to build lockfile contents by translating a sibling npm
/// `package-lock.json` that lives next to `deno_lockfile_path`. Returns
/// `None` when there's nothing usable to seed from (npm lockfile missing,
/// unreadable, an unsupported version, or containing no translatable
/// packages), in which case the caller should fall back to an empty
/// lockfile.
pub fn seed_npm_lockfile_content<TSys: LockfileSys>(
  sys: &TSys,
  deno_lockfile_path: &Path,
) -> Option<LockfileContent> {
  let dir = deno_lockfile_path.parent()?;
  let npm_lockfile_text = sys
    .fs_read_to_string(dir.join(NPM_LOCKFILE_FILE_NAME))
    .ok()?;
  let npm_lockfile: Value = serde_json::from_str(&npm_lockfile_text).ok()?;
  let npm_lockfile = npm_lockfile.as_object()?;
  match npm_lockfile.get("lockfileVersion").and_then(Value::as_u64) {
    // `packages` (a flat map keyed by node_modules path) exists in both
    // lockfileVersion 2 and 3, unlike the legacy nested `dependencies` tree.
    Some(2) | Some(3) => {}
    _ => return None,
  }
  let packages = npm_lockfile.get("packages")?.as_object()?;

  let mut valid_entries = HashMap::with_capacity(packages.len());
  for (path, entry) in packages {
    if path.is_empty() {
      // the root project itself, not a dependency to seed
      continue;
    }
    if let Some(valid_entry) = as_valid_entry(path, entry) {
      valid_entries.insert(path.as_str(), valid_entry);
    }
  }
  if valid_entries.is_empty() {
    return None;
  }

  let npm = build_npm_packages(packages, &valid_entries);
  let specifiers = build_specifiers(sys, dir, packages, &valid_entries);

  let mut content = LockfileContent::default();
  content.packages = PackagesContent {
    specifiers,
    jsr: Default::default(),
    npm,
  };
  Some(content)
}

/// Validates that a `package-lock.json` entry refers to an npm-registry
/// package with an integrity hash, and computes its `deno_lockfile` id
/// (`name@version`). Workspace links (`"link": true`) and non-registry
/// dependencies (`file:`, `git+...`, direct tarball URLs, etc.) are rejected
/// since they aren't representable the same way in a `deno.lock`, and
/// packages missing an integrity hash are rejected since it can't be
/// verified.
fn as_valid_entry(path: &str, entry: &Value) -> Option<ValidEntry> {
  let obj = entry.as_object()?;
  if obj.get("link").and_then(Value::as_bool).unwrap_or(false) {
    return None;
  }
  let resolved = obj.get("resolved").and_then(Value::as_str)?;
  if !is_registry_tarball_url(resolved) {
    return None;
  }
  let integrity = obj.get("integrity").and_then(Value::as_str)?;
  if integrity.is_empty() {
    return None;
  }
  let version = obj.get("version").and_then(Value::as_str)?;
  Version::parse_from_npm(version).ok()?;
  let name = obj
    .get("name")
    .and_then(Value::as_str)
    .unwrap_or_else(|| package_name_from_path(path));
  if name.is_empty() {
    return None;
  }
  Some(ValidEntry {
    name: StackString::from(name),
    version: StackString::from(version),
  })
}

/// Registry tarball URLs (npmjs.org and npm-registry-compatible hosts like
/// private registries) always separate the package name from the file name
/// with `/-/`, e.g. `https://registry.npmjs.org/left-pad/-/left-pad-1.3.0.tgz`.
/// A direct tarball or git URL dependency (specified verbatim in
/// package.json) doesn't follow this convention.
fn is_registry_tarball_url(resolved: &str) -> bool {
  (resolved.starts_with("http://") || resolved.starts_with("https://"))
    && resolved.contains("/-/")
}

/// The package name implied by a `packages` map key, e.g.
/// `node_modules/foo/node_modules/@scope/bar` -> `@scope/bar`. Overridden by
/// an explicit `"name"` field on the entry for aliased dependencies (e.g.
/// `"my-pad": "npm:left-pad@^1.3.0"` is keyed by `node_modules/my-pad` but
/// names itself `left-pad`).
fn package_name_from_path(path: &str) -> &str {
  match path.rfind("node_modules/") {
    Some(idx) => &path[idx + "node_modules/".len()..],
    None => path,
  }
}

/// Finds the `packages` map key that `dep_name` resolves to from a package
/// located at `from_path`, following Node's node_modules resolution
/// algorithm: check the current package's own nested `node_modules/`
/// first, then walk up through each ancestor's `node_modules/` until the
/// root is reached.
fn find_dep_path(
  packages: &Map<String, Value>,
  from_path: &str,
  dep_name: &str,
) -> Option<String> {
  let mut context = from_path;
  loop {
    let candidate = if context.is_empty() {
      format!("node_modules/{dep_name}")
    } else {
      format!("{context}/node_modules/{dep_name}")
    };
    if packages.contains_key(&candidate) {
      return Some(candidate);
    }
    if context.is_empty() {
      return None;
    }
    context = strip_last_node_modules_segment(context);
  }
}

fn strip_last_node_modules_segment(path: &str) -> &str {
  match path.rfind("node_modules/") {
    Some(0) => "",
    Some(idx) => &path[..idx - 1],
    None => "",
  }
}

fn string_array(value: Option<&Value>) -> Vec<SmallStackString> {
  value
    .and_then(Value::as_array)
    .map(|values| {
      values
        .iter()
        .filter_map(Value::as_str)
        .map(SmallStackString::from)
        .collect()
    })
    .unwrap_or_default()
}

/// Resolves a `dependencies`/`optionalDependencies`-shaped map (alias name
/// -> version range) from a package's entry into the `deno_lockfile`
/// representation (alias name -> resolved `name@version` id), dropping any
/// dependency that can't be resolved to a valid, translated package.
fn resolve_dep_map(
  packages: &Map<String, Value>,
  valid_entries: &HashMap<&str, ValidEntry>,
  from_path: &str,
  deps: Option<&Value>,
) -> BTreeMap<StackString, StackString> {
  let mut result = BTreeMap::new();
  let Some(deps) = deps.and_then(Value::as_object) else {
    return result;
  };
  for alias in deps.keys() {
    let Some(target_path) = find_dep_path(packages, from_path, alias) else {
      continue;
    };
    let Some(target) = valid_entries.get(target_path.as_str()) else {
      continue;
    };
    result.insert(StackString::from(alias.as_str()), target.id());
  }
  result
}

/// Resolves the subset of a package's `peerDependencies` that are marked
/// optional in `peerDependenciesMeta`, matching `deno_lockfile`'s
/// `optionalPeers` semantics.
fn resolve_optional_peer_map(
  packages: &Map<String, Value>,
  valid_entries: &HashMap<&str, ValidEntry>,
  from_path: &str,
  obj: &Map<String, Value>,
) -> BTreeMap<StackString, StackString> {
  let mut result = BTreeMap::new();
  let Some(peer_deps) = obj.get("peerDependencies").and_then(Value::as_object)
  else {
    return result;
  };
  let Some(peer_deps_meta) =
    obj.get("peerDependenciesMeta").and_then(Value::as_object)
  else {
    return result;
  };
  for (name, meta) in peer_deps_meta {
    let is_optional = meta
      .as_object()
      .and_then(|meta| meta.get("optional"))
      .and_then(Value::as_bool)
      .unwrap_or(false);
    if !is_optional || !peer_deps.contains_key(name) {
      continue;
    }
    let Some(target_path) = find_dep_path(packages, from_path, name) else {
      continue;
    };
    let Some(target) = valid_entries.get(target_path.as_str()) else {
      continue;
    };
    result.insert(StackString::from(name.as_str()), target.id());
  }
  result
}

fn build_npm_packages(
  packages: &Map<String, Value>,
  valid_entries: &HashMap<&str, ValidEntry>,
) -> BTreeMap<StackString, NpmPackageInfo> {
  let mut npm = BTreeMap::new();
  for (path, valid_entry) in valid_entries {
    // already validated to be an object in `as_valid_entry`
    let obj = packages[*path].as_object().unwrap();
    let dependencies =
      resolve_dep_map(packages, valid_entries, path, obj.get("dependencies"));
    let optional_dependencies = resolve_dep_map(
      packages,
      valid_entries,
      path,
      obj.get("optionalDependencies"),
    );
    let optional_peers =
      resolve_optional_peer_map(packages, valid_entries, path, obj);
    let integrity = obj
      .get("integrity")
      .and_then(Value::as_str)
      .map(str::to_string);
    let deprecated = obj.get("deprecated").is_some_and(Value::is_string);
    let scripts = obj
      .get("hasInstallScript")
      .and_then(Value::as_bool)
      .unwrap_or(false);
    let bin = obj.get("bin").is_some_and(Value::is_object);

    npm.insert(
      valid_entry.id(),
      NpmPackageInfo {
        integrity,
        dependencies,
        optional_dependencies,
        optional_peers,
        os: string_array(obj.get("os")),
        cpu: string_array(obj.get("cpu")),
        tarball: None,
        deprecated,
        scripts,
        bin,
      },
    );
  }
  npm
}

/// Builds the `specifiers` section (dependency requirement -> resolved
/// version) from the root `package.json`'s `dependencies`/`devDependencies`,
/// which is what lets the seeded npm packages actually be reached during
/// resolution (only packages reachable from a specifier are considered).
fn build_specifiers<TSys: LockfileSys>(
  sys: &TSys,
  dir: &Path,
  packages: &Map<String, Value>,
  valid_entries: &HashMap<&str, ValidEntry>,
) -> HashMap<JsrDepPackageReq, SmallStackString> {
  let mut specifiers = HashMap::new();
  let Ok(Some(package_json)) =
    PackageJson::load_from_path(sys, None, &dir.join(PACKAGE_JSON_FILE_NAME))
  else {
    return specifiers;
  };
  let deps = package_json.resolve_local_package_json_deps();
  for (alias, dep) in
    deps.dependencies.iter().chain(deps.dev_dependencies.iter())
  {
    let Ok(PackageJsonDepValue::Req(req)) = dep else {
      // file:/workspace:/catalog: deps (or unparsable ones) aren't seeded
      continue;
    };
    let Some(target_path) = find_dep_path(packages, "", alias) else {
      continue;
    };
    let Some(target) = valid_entries.get(target_path.as_str()) else {
      continue;
    };
    specifiers.insert(
      JsrDepPackageReq::npm(req.clone()),
      SmallStackString::from(target.version.as_str()),
    );
  }
  specifiers
}

#[cfg(test)]
mod tests {
  use deno_semver::package::PackageReq;
  use serde_json::json;
  use sys_traits::impls::InMemorySys;

  use super::*;

  fn setup() -> (InMemorySys, std::path::PathBuf) {
    let cwd = std::path::PathBuf::from("/project");
    let sys = InMemorySys::new_with_cwd(&cwd);
    sys.fs_insert_json(
      cwd.join("package.json"),
      json!({
        "dependencies": {
          "left-pad": "^1.3.0",
          "esbuild": "^0.1.0",
          "debug": "^4.0.0",
          "pkg-a": "file:./pkg-a",
        },
        "devDependencies": {
          "is-odd": "^1.0.0",
        },
      }),
    );
    sys.fs_insert_json(
      cwd.join("package-lock.json"),
      json!({
        "lockfileVersion": 3,
        "packages": {
          "": {
            "dependencies": {
              "left-pad": "^1.3.0",
              "esbuild": "^0.1.0",
              "debug": "^4.0.0",
              "pkg-a": "file:./pkg-a",
            },
            "devDependencies": {
              "is-odd": "^1.0.0",
            },
          },
          "node_modules/left-pad": {
            "version": "1.3.0",
            "resolved": "https://registry.npmjs.org/left-pad/-/left-pad-1.3.0.tgz",
            "integrity": "sha512-left-pad",
          },
          "node_modules/is-odd": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/is-odd/-/is-odd-1.0.0.tgz",
            "integrity": "sha512-is-odd",
            "dependencies": {
              "is-number": "^3.0.0",
            },
          },
          "node_modules/is-odd/node_modules/is-number": {
            "version": "4.0.0",
            "resolved": "https://registry.npmjs.org/is-number/-/is-number-4.0.0.tgz",
            "integrity": "sha512-is-number-4",
          },
          "node_modules/is-number": {
            "version": "3.0.0",
            "resolved": "https://registry.npmjs.org/is-number/-/is-number-3.0.0.tgz",
            "integrity": "sha512-is-number-3",
          },
          "node_modules/esbuild": {
            "version": "0.1.0",
            "resolved": "https://registry.npmjs.org/esbuild/-/esbuild-0.1.0.tgz",
            "integrity": "sha512-esbuild",
            "hasInstallScript": true,
            "bin": { "esbuild": "bin/esbuild" },
            "optionalDependencies": {
              "@esbuild/linux-x64": "0.1.0",
            },
          },
          "node_modules/@esbuild/linux-x64": {
            "version": "0.1.0",
            "resolved": "https://registry.npmjs.org/@esbuild/linux-x64/-/linux-x64-0.1.0.tgz",
            "integrity": "sha512-esbuild-linux-x64",
            "optional": true,
            "os": ["linux"],
            "cpu": ["x64"],
          },
          "node_modules/debug": {
            "version": "4.0.0",
            "resolved": "https://registry.npmjs.org/debug/-/debug-4.0.0.tgz",
            "integrity": "sha512-debug",
            "dependencies": {
              "ms": "^2.0.0",
            },
            "peerDependencies": {
              "supports-color": "^7",
            },
            "peerDependenciesMeta": {
              "supports-color": { "optional": true },
            },
          },
          "node_modules/ms": {
            "version": "2.0.0",
            "resolved": "https://registry.npmjs.org/ms/-/ms-2.0.0.tgz",
            "integrity": "sha512-ms",
          },
          "node_modules/supports-color": {
            "version": "7.0.0",
            "resolved": "https://registry.npmjs.org/supports-color/-/supports-color-7.0.0.tgz",
            "integrity": "sha512-supports-color",
          },
          "node_modules/pkg-a": {
            "resolved": "pkg-a",
            "link": true,
          },
          "pkg-a": {
            "version": "1.0.0",
          },
          "node_modules/from-git": {
            "version": "9.9.9",
            "resolved": "git+https://github.com/example/from-git.git",
            "integrity": "sha512-from-git",
          },
          "node_modules/no-integrity": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/no-integrity/-/no-integrity-1.0.0.tgz",
          },
        },
      }),
    );
    (sys, cwd)
  }

  #[test]
  fn seeds_npm_packages_and_specifiers_from_npm_lockfile() {
    let (sys, cwd) = setup();

    let content =
      seed_npm_lockfile_content(&sys, &cwd.join("deno.lock")).unwrap();

    let npm = &content.packages.npm;
    // non-registry / unverifiable entries are excluded
    assert!(!npm.contains_key("pkg-a@1.0.0"));
    assert!(!npm.contains_key("from-git@9.9.9"));
    assert!(!npm.contains_key("no-integrity@1.0.0"));

    // regular dependency, including nested (non-hoisted) resolution: the
    // direct `node_modules/is-number` (3.0.0) differs from the one nested
    // under `is-odd` (4.0.0), and `is-odd` must reference its own nested one
    let is_odd = npm.get("is-odd@1.0.0").unwrap();
    assert_eq!(
      is_odd.dependencies.get("is-number").unwrap().as_str(),
      "is-number@4.0.0"
    );
    assert!(npm.contains_key("is-number@3.0.0"));
    assert!(npm.contains_key("is-number@4.0.0"));

    // optional dependency + os/cpu constraints
    let esbuild = npm.get("esbuild@0.1.0").unwrap();
    assert!(esbuild.dependencies.is_empty());
    assert_eq!(
      esbuild
        .optional_dependencies
        .get("@esbuild/linux-x64")
        .unwrap()
        .as_str(),
      "@esbuild/linux-x64@0.1.0"
    );
    assert!(esbuild.scripts);
    assert!(esbuild.bin);
    let esbuild_linux_x64 = npm.get("@esbuild/linux-x64@0.1.0").unwrap();
    assert_eq!(esbuild_linux_x64.os, vec![SmallStackString::from("linux")]);
    assert_eq!(esbuild_linux_x64.cpu, vec![SmallStackString::from("x64")]);

    // optional peer dependency
    let debug = npm.get("debug@4.0.0").unwrap();
    assert_eq!(debug.dependencies.get("ms").unwrap().as_str(), "ms@2.0.0");
    assert_eq!(
      debug.optional_peers.get("supports-color").unwrap().as_str(),
      "supports-color@7.0.0"
    );

    // specifiers reachable from package.json's dependencies/devDependencies
    let specifiers = &content.packages.specifiers;
    assert_eq!(
      specifiers
        .get(&JsrDepPackageReq::npm(
          PackageReq::from_str("left-pad@^1.3.0").unwrap()
        ))
        .unwrap()
        .as_str(),
      "1.3.0"
    );
    assert_eq!(
      specifiers
        .get(&JsrDepPackageReq::npm(
          PackageReq::from_str("is-odd@^1.0.0").unwrap()
        ))
        .unwrap()
        .as_str(),
      "1.0.0"
    );
    // `pkg-a` is a `file:` dependency, so it's not seeded
    assert!(!specifiers.iter().any(|(req, _)| req.req.name == "pkg-a"));
  }

  #[test]
  fn returns_none_when_no_npm_lockfile_exists() {
    let cwd = std::path::PathBuf::from("/project");
    let sys = InMemorySys::new_with_cwd(&cwd);
    assert!(seed_npm_lockfile_content(&sys, &cwd.join("deno.lock")).is_none());
  }

  #[test]
  fn returns_none_for_unsupported_lockfile_version() {
    let cwd = std::path::PathBuf::from("/project");
    let sys = InMemorySys::new_with_cwd(&cwd);
    sys.fs_insert_json(
      cwd.join("package-lock.json"),
      json!({
        "lockfileVersion": 1,
        "dependencies": {
          "left-pad": { "version": "1.3.0" },
        },
      }),
    );
    assert!(seed_npm_lockfile_content(&sys, &cwd.join("deno.lock")).is_none());
  }
}
