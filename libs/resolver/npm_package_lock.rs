// Copyright 2018-2026 the Deno authors. MIT license.

//! Translation of an npm `package-lock.json` (lockfileVersion 2 or 3) into
//! deno lockfile content.
//!
//! This is used to seed npm resolution the first time a local `deno install`
//! runs in a project that has a `package-lock.json`, but no `deno.lock` yet
//! (e.g. a project migrating from npm). Seeding preserves the exact versions
//! and integrity hashes that npm had already pinned instead of re-resolving
//! everything from scratch.

use std::collections::BTreeMap;
use std::collections::HashMap;
use std::collections::HashSet;
use std::collections::VecDeque;

use anyhow::Context;
use anyhow::Error as AnyError;
use anyhow::bail;
use deno_lockfile::LockfileContent;
use deno_lockfile::NpmPackageInfo;
use deno_npm::resolution::DefaultTarballUrlProvider;
use deno_npm::resolution::NpmRegistryDefaultTarballUrlProvider;
use deno_npm::resolution::SnapshotFromLockfileParams;
use deno_package_json::PackageJsonDepValue;
use deno_semver::SmallStackString;
use deno_semver::StackString;
use deno_semver::Version;
use deno_semver::jsr::JsrDepPackageReq;
use deno_semver::package::PackageNv;
use serde::Deserialize;

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct NpmPackageLock {
  lockfile_version: Option<u64>,
  #[serde(default)]
  packages: BTreeMap<String, NpmPackageLockEntry>,
}

#[derive(Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
struct NpmPackageLockEntry {
  /// Real package name. Only set when it differs from the name derived
  /// from the entry's `node_modules` path (npm aliases) and for workspace
  /// member entries.
  name: Option<String>,
  version: Option<String>,
  resolved: Option<String>,
  integrity: Option<String>,
  /// `true` for entries that are symlinks to another entry (e.g. workspace
  /// members linked into `node_modules`).
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
  peer_dependencies_meta: BTreeMap<String, NpmPackageLockPeerMeta>,
  #[serde(default)]
  os: Vec<SmallStackString>,
  #[serde(default)]
  cpu: Vec<SmallStackString>,
  #[serde(default)]
  has_install_script: bool,
  /// Can be a string or an object, only its presence matters.
  bin: Option<serde_json::Value>,
}

#[derive(Debug, Default, Deserialize)]
struct NpmPackageLockPeerMeta {
  #[serde(default)]
  optional: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum DepKind {
  Regular,
  Optional,
  Peer,
  OptionalPeer,
}

#[derive(Debug)]
struct ResolvedDep<'a> {
  alias: &'a str,
  kind: DepKind,
  /// Path of the entry this dependency resolved to. `None` when the
  /// dependency could not be resolved to a translatable registry package.
  target: Option<&'a str>,
}

/// A `node_modules` entry that came from an npm registry and can be
/// translated to a deno lockfile entry.
#[derive(Debug)]
struct Node<'a> {
  entry: &'a NpmPackageLockEntry,
  nv: PackageNv,
  /// Serialized `name@version` id used in the deno lockfile.
  id: StackString,
  deps: Vec<ResolvedDep<'a>>,
}

/// Translates the text of an npm `package-lock.json` into the content of a
/// deno lockfile, keeping only packages that are reachable from the provided
/// workspace dependency requirements.
///
/// Entries that don't come from an npm registry (workspace links, file:,
/// git:, http(s) tarball and similar dependencies) and packages without an
/// integrity hash are skipped, along with any package that requires them as
/// a regular dependency.
pub fn lockfile_content_from_npm_package_lock_json(
  text: &str,
  workspace_deps: &HashSet<JsrDepPackageReq>,
) -> Result<LockfileContent, AnyError> {
  let lock: NpmPackageLock =
    serde_json::from_str(text).context("failed parsing as JSON")?;
  if !matches!(lock.lockfile_version, Some(2 | 3)) {
    bail!(
      "unsupported lockfileVersion: {}",
      lock
        .lockfile_version
        .map(|v| v.to_string())
        .unwrap_or_else(|| "<missing>".to_string())
    );
  }

  // collect the entries that represent installed registry packages, keyed
  // by their path in the tree
  let mut nodes: HashMap<&str, Node> = HashMap::new();
  for (path, entry) in &lock.packages {
    if entry.link || !is_node_modules_path(path) {
      continue;
    }
    let Some(alias) = package_alias_from_path(path) else {
      continue;
    };
    let Some(node) = registry_node(alias, entry) else {
      continue;
    };
    nodes.insert(path.as_str(), node);
  }

  // resolve every node's dependencies against the tree the same way node
  // resolution does: the closest `node_modules/<alias>` folder at or above
  // the package's own folder wins
  let mut resolved_deps_by_path: HashMap<&str, Vec<ResolvedDep>> =
    HashMap::with_capacity(nodes.len());
  for (&path, node) in &nodes {
    let mut deps = Vec::new();
    for (alias, (kind, range)) in node_dep_ranges(node.entry) {
      let target =
        resolve_dep_target(&lock.packages, &nodes, path, alias, range);
      deps.push(ResolvedDep {
        alias,
        kind,
        target,
      });
    }
    resolved_deps_by_path.insert(path, deps);
  }
  for (path, deps) in resolved_deps_by_path {
    nodes.get_mut(path).unwrap().deps = deps;
  }

  // a package whose regular dependency can't be translated can't be
  // translated itself; propagate that through dependents
  let invalid_paths = invalidate_incomplete_nodes(&nodes);

  let mut content = LockfileContent::default();

  // top level requirements come from the dependencies and devDependencies
  // of the importers (the root project and workspace members), matching
  // the package.json dependencies deno tracks in the workspace
  for (importer_path, entry) in &lock.packages {
    if entry.link || is_node_modules_path(importer_path) {
      continue;
    }
    for (alias, range) in entry
      .dependencies
      .iter()
      .chain(entry.dev_dependencies.iter())
    {
      let Ok(PackageJsonDepValue::Req(req)) =
        PackageJsonDepValue::parse(alias, range)
      else {
        continue;
      };
      let dep_req = JsrDepPackageReq::npm(req);
      // only seed requirements the workspace actually tracks so stale
      // entries in the npm lockfile don't get carried over
      if !workspace_deps.contains(&dep_req) {
        continue;
      }
      let Some(target_path) =
        resolve_dep_entry_path(&lock.packages, importer_path, alias)
      else {
        continue;
      };
      if invalid_paths.contains(target_path) {
        continue;
      }
      let Some(node) = nodes.get(target_path) else {
        continue;
      };
      if !version_matches_req(&dep_req.req, node) {
        continue;
      }
      content
        .packages
        .specifiers
        .entry(dep_req)
        .or_insert_with(|| {
          SmallStackString::from_string(node.nv.version.to_string())
        });
    }
  }

  // now emit the packages, iterating in path order so that for packages
  // that appear multiple times in the tree with the same version the copy
  // hoisted highest wins
  for path in lock.packages.keys() {
    let Some(node) = nodes.get(path.as_str()) else {
      continue;
    };
    if invalid_paths.contains(path.as_str()) {
      continue;
    }
    if content.packages.npm.contains_key(&node.id) {
      continue;
    }
    let mut dependencies = BTreeMap::new();
    let mut optional_dependencies = BTreeMap::new();
    let mut optional_peers = BTreeMap::new();
    for dep in &node.deps {
      let Some(target_path) = dep.target else {
        continue;
      };
      if invalid_paths.contains(target_path) {
        continue;
      }
      let target_id = nodes.get(target_path).unwrap().id.clone();
      let alias = StackString::from_str(dep.alias);
      match dep.kind {
        DepKind::Regular | DepKind::Peer => {
          dependencies.insert(alias, target_id);
        }
        DepKind::Optional => {
          optional_dependencies.insert(alias, target_id);
        }
        DepKind::OptionalPeer => {
          // a resolved optional peer is recorded both as a regular
          // dependency edge and in optionalPeers, matching how deno
          // writes lockfiles from a resolution snapshot
          dependencies.insert(alias.clone(), target_id.clone());
          optional_peers.insert(alias, target_id);
        }
      }
    }
    content.packages.npm.insert(
      node.id.clone(),
      NpmPackageInfo {
        integrity: node.entry.integrity.clone(),
        dependencies,
        optional_dependencies,
        optional_peers,
        os: node.entry.os.clone(),
        cpu: node.entry.cpu.clone(),
        tarball: tarball_url(node),
        deprecated: false,
        scripts: node.entry.has_install_script,
        bin: node.entry.bin.is_some(),
      },
    );
  }

  retain_reachable_packages(&mut content);

  // finally, ensure the resulting content actually loads as a valid
  // resolution snapshot so a bad translation can never fail the install
  validate_content(&content)?;

  Ok(content)
}

fn is_node_modules_path(path: &str) -> bool {
  path.starts_with("node_modules/") || path.contains("/node_modules/")
}

/// Extracts the package name from the final `node_modules/<name>` part of
/// an entry path (e.g. `node_modules/a/node_modules/@scope/b` -> `@scope/b`).
fn package_alias_from_path(path: &str) -> Option<&str> {
  let index = path.rfind("node_modules/")?;
  if index != 0 && !path[..index].ends_with('/') {
    return None;
  }
  let alias = &path[index + "node_modules/".len()..];
  let is_valid = match alias.strip_prefix('@') {
    Some(rest) => match rest.split_once('/') {
      Some((scope, name)) => {
        !scope.is_empty() && !name.is_empty() && !name.contains('/')
      }
      None => false,
    },
    None => !alias.is_empty() && !alias.contains('/'),
  };
  if is_valid { Some(alias) } else { None }
}

/// Creates a node for an entry that represents a package downloaded from an
/// npm registry. Returns `None` for anything else (git, file and remote
/// tarball dependencies, bundled packages without their own integrity, etc.).
fn registry_node<'a>(
  alias: &str,
  entry: &'a NpmPackageLockEntry,
) -> Option<Node<'a>> {
  let version = Version::parse_from_npm(entry.version.as_deref()?).ok()?;
  // registry packages always have an integrity hash; this also naturally
  // excludes git dependencies and bundled packages
  if entry.integrity.as_deref().unwrap_or_default().is_empty() {
    return None;
  }
  if let Some(resolved) = &entry.resolved {
    // registry tarballs are always fetched over http(s); anything else
    // (git+https://, file:, relative paths, ...) is not a registry package
    if !resolved.starts_with("https://") && !resolved.starts_with("http://") {
      return None;
    }
  }
  let name = entry.name.as_deref().unwrap_or(alias);
  let nv = PackageNv {
    name: StackString::from_str(name),
    version,
  };
  let id = StackString::from_string(nv.to_string());
  Some(Node {
    entry,
    nv,
    id,
    deps: Vec::new(),
  })
}

/// Collects the dependency ranges of an installed package keyed by alias.
/// When a name appears in multiple sections, optionalDependencies wins over
/// dependencies which wins over peerDependencies (matching npm semantics).
fn node_dep_ranges(
  entry: &NpmPackageLockEntry,
) -> BTreeMap<&str, (DepKind, &str)> {
  let mut result = BTreeMap::new();
  for (alias, range) in &entry.peer_dependencies {
    let is_optional = entry
      .peer_dependencies_meta
      .get(alias)
      .map(|meta| meta.optional)
      .unwrap_or(false);
    let kind = if is_optional {
      DepKind::OptionalPeer
    } else {
      DepKind::Peer
    };
    result.insert(alias.as_str(), (kind, range.as_str()));
  }
  for (alias, range) in &entry.dependencies {
    result.insert(alias.as_str(), (DepKind::Regular, range.as_str()));
  }
  for (alias, range) in &entry.optional_dependencies {
    result.insert(alias.as_str(), (DepKind::Optional, range.as_str()));
  }
  result
}

/// Finds the entry a dependency resolves to by walking up the tree checking
/// each `node_modules` folder, like node resolution does.
fn resolve_dep_entry_path<'a>(
  packages: &'a BTreeMap<String, NpmPackageLockEntry>,
  from_path: &str,
  alias: &str,
) -> Option<&'a str> {
  let mut base = from_path;
  loop {
    let candidate = if base.is_empty() {
      format!("node_modules/{}", alias)
    } else {
      format!("{}/node_modules/{}", base, alias)
    };
    if let Some((path, _)) = packages.get_key_value(candidate.as_str()) {
      return Some(path.as_str());
    }
    if base.is_empty() {
      return None;
    }
    base = match base.rfind('/') {
      Some(index) => &base[..index],
      None => "",
    };
  }
}

/// Resolves a dependency of an installed package to the path of a
/// translatable registry package, or `None` when the dependency uses a
/// non-registry specifier (git, file, http tarball, workspace, ...) or
/// resolves to an entry that can't be translated.
fn resolve_dep_target<'a>(
  packages: &'a BTreeMap<String, NpmPackageLockEntry>,
  nodes: &HashMap<&str, Node>,
  from_path: &str,
  alias: &'a str,
  range: &str,
) -> Option<&'a str> {
  let Ok(PackageJsonDepValue::Req(req)) =
    PackageJsonDepValue::parse(alias, range)
  else {
    return None;
  };
  let target_path = resolve_dep_entry_path(packages, from_path, alias)?;
  let node = nodes.get(target_path)?;
  if node.nv.name != req.name || !version_matches_req(&req, node) {
    return None;
  }
  Some(target_path)
}

fn version_matches_req(
  req: &deno_semver::package::PackageReq,
  node: &Node,
) -> bool {
  // a dist tag can't be verified locally, but npm resolved it when the
  // lockfile was written, so trust the pinned version
  req.version_req.tag().is_some() || req.version_req.matches(&node.nv.version)
}

/// Marks nodes that have an unresolvable regular dependency as invalid and
/// propagates the invalidation to all packages that regularly depend on
/// them. Unresolvable optional and peer dependency edges are simply dropped
/// instead.
fn invalidate_incomplete_nodes<'a>(
  nodes: &HashMap<&'a str, Node<'a>>,
) -> HashSet<&'a str> {
  let mut dependents_by_path: HashMap<&'a str, Vec<&'a str>> = HashMap::new();
  let mut invalid: HashSet<&'a str> = HashSet::new();
  let mut pending: VecDeque<&'a str> = VecDeque::new();
  for (&path, node) in nodes {
    for dep in &node.deps {
      if dep.kind != DepKind::Regular {
        continue;
      }
      match dep.target {
        Some(target) => {
          dependents_by_path.entry(target).or_default().push(path);
        }
        None => {
          if invalid.insert(path) {
            pending.push_back(path);
          }
        }
      }
    }
  }
  while let Some(path) = pending.pop_front() {
    if let Some(dependents) = dependents_by_path.get(path) {
      for &dependent in dependents {
        if invalid.insert(dependent) {
          pending.push_back(dependent);
        }
      }
    }
  }
  invalid
}

/// Omits the tarball url when it's the default url of the public npm
/// registry, the same way deno does when writing a lockfile.
fn tarball_url(node: &Node) -> Option<StackString> {
  let resolved = node.entry.resolved.as_deref()?;
  let default_url =
    NpmRegistryDefaultTarballUrlProvider.default_tarball_url(&node.nv);
  if resolved == default_url {
    None
  } else {
    Some(StackString::from_str(resolved))
  }
}

/// Keeps only the npm packages that are reachable from the seeded
/// requirements, dropping subtrees that deno resolution would never look at.
fn retain_reachable_packages(content: &mut LockfileContent) {
  let mut reachable: HashSet<StackString> = HashSet::new();
  let mut pending: VecDeque<StackString> = VecDeque::new();
  for (req, version) in &content.packages.specifiers {
    let mut id =
      StackString::with_capacity(req.req.name.len() + 1 + version.len());
    id.push_str(&req.req.name);
    id.push('@');
    id.push_str(version);
    if reachable.insert(id.clone()) {
      pending.push_back(id);
    }
  }
  while let Some(id) = pending.pop_front() {
    let Some(package) = content.packages.npm.get(&id) else {
      continue;
    };
    for dep_id in package
      .dependencies
      .values()
      .chain(package.optional_dependencies.values())
    {
      if reachable.insert(dep_id.clone()) {
        pending.push_back(dep_id.clone());
      }
    }
  }
  content.packages.npm.retain(|id, _| reachable.contains(id));
}

/// Ensures the translated content loads as a valid npm resolution snapshot,
/// which is what deno will do with the seeded lockfile right after.
fn validate_content(content: &LockfileContent) -> Result<(), AnyError> {
  let lockfile = deno_lockfile::Lockfile {
    overwrite: false,
    has_content_changed: false,
    content: content.clone(),
    filename: std::path::PathBuf::new(),
  };
  deno_npm::resolution::snapshot_from_lockfile(SnapshotFromLockfileParams {
    link_packages: &Default::default(),
    lockfile: &lockfile,
    default_tarball_url: Default::default(),
    dedup_equivalent_peer_variants: false,
  })
  .context("failed loading translated content as a resolution snapshot")?;
  Ok(())
}

#[cfg(test)]
mod test {
  use super::*;

  fn translate(
    package_lock: serde_json::Value,
    workspace_deps: &[&str],
  ) -> Result<String, AnyError> {
    let workspace_deps = workspace_deps
      .iter()
      .map(|dep| JsrDepPackageReq::from_str(dep).unwrap())
      .collect::<HashSet<_>>();
    let content = lockfile_content_from_npm_package_lock_json(
      &package_lock.to_string(),
      &workspace_deps,
    )?;
    let lockfile = deno_lockfile::Lockfile {
      overwrite: false,
      has_content_changed: false,
      content,
      filename: std::path::PathBuf::new(),
    };
    Ok(lockfile.as_json_string())
  }

  fn assert_translates(
    package_lock: serde_json::Value,
    workspace_deps: &[&str],
    expected: serde_json::Value,
  ) {
    let expected = {
      let content = LockfileContent::from_json(expected).unwrap();
      deno_lockfile::Lockfile {
        overwrite: false,
        has_content_changed: false,
        content,
        filename: std::path::PathBuf::new(),
      }
      .as_json_string()
    };
    assert_eq!(translate(package_lock, workspace_deps).unwrap(), expected);
  }

  #[test]
  fn basic_with_scoped_and_dev_deps() {
    assert_translates(
      serde_json::json!({
        "name": "app",
        "lockfileVersion": 3,
        "requires": true,
        "packages": {
          "": {
            "name": "app",
            "dependencies": { "@denotest/a": "^1.0.0" },
            "devDependencies": { "b": "~2.1.0" }
          },
          "node_modules/@denotest/a": {
            "version": "1.2.3",
            "resolved": "https://registry.npmjs.org/@denotest/a/-/a-1.2.3.tgz",
            "integrity": "sha512-aaa",
            "dependencies": { "c": "^3.0.0" }
          },
          "node_modules/b": {
            "version": "2.1.5",
            "resolved": "http://localhost:4260/b/2.1.5.tgz",
            "integrity": "sha512-bbb",
            "dev": true
          },
          "node_modules/c": {
            "version": "3.4.5",
            "resolved": "https://registry.npmjs.org/c/-/c-3.4.5.tgz",
            "integrity": "sha512-ccc"
          }
        }
      }),
      &["npm:@denotest/a@^1.0.0", "npm:b@~2.1.0"],
      serde_json::json!({
        "version": "5",
        "specifiers": {
          "npm:@denotest/a@^1.0.0": "1.2.3",
          "npm:b@~2.1.0": "2.1.5"
        },
        "npm": {
          "@denotest/a@1.2.3": {
            "integrity": "sha512-aaa",
            "dependencies": ["c"]
          },
          "b@2.1.5": {
            "integrity": "sha512-bbb",
            // not the default registry tarball url, so it's kept
            "tarball": "http://localhost:4260/b/2.1.5.tgz"
          },
          "c@3.4.5": {
            "integrity": "sha512-ccc"
          }
        }
      }),
    );
  }

  #[test]
  fn nested_node_modules_resolution() {
    assert_translates(
      serde_json::json!({
        "lockfileVersion": 3,
        "packages": {
          "": {
            "dependencies": { "a": "1.0.0", "b": "1.0.0" }
          },
          "node_modules/a": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/a/-/a-1.0.0.tgz",
            "integrity": "sha512-a",
            "dependencies": { "c": "^1.0.0" }
          },
          "node_modules/b": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/b/-/b-1.0.0.tgz",
            "integrity": "sha512-b",
            "dependencies": { "c": "^2.0.0" }
          },
          "node_modules/b/node_modules/c": {
            "version": "2.3.0",
            "resolved": "https://registry.npmjs.org/c/-/c-2.3.0.tgz",
            "integrity": "sha512-c2"
          },
          "node_modules/c": {
            "version": "1.5.0",
            "resolved": "https://registry.npmjs.org/c/-/c-1.5.0.tgz",
            "integrity": "sha512-c1"
          }
        }
      }),
      &["npm:a@1.0.0", "npm:b@1.0.0"],
      serde_json::json!({
        "version": "5",
        "specifiers": {
          "npm:a@1.0.0": "1.0.0",
          "npm:b@1.0.0": "1.0.0"
        },
        "npm": {
          "a@1.0.0": {
            "integrity": "sha512-a",
            "dependencies": ["c@1.5.0"]
          },
          "b@1.0.0": {
            "integrity": "sha512-b",
            "dependencies": ["c@2.3.0"]
          },
          "c@1.5.0": {
            "integrity": "sha512-c1"
          },
          "c@2.3.0": {
            "integrity": "sha512-c2"
          }
        }
      }),
    );
  }

  #[test]
  fn aliased_packages() {
    assert_translates(
      serde_json::json!({
        "lockfileVersion": 3,
        "packages": {
          "": {
            "dependencies": {
              "my-a": "npm:a@^1.0.0",
              "user": "^1.0.0"
            }
          },
          "node_modules/my-a": {
            "name": "a",
            "version": "1.1.0",
            "resolved": "https://registry.npmjs.org/a/-/a-1.1.0.tgz",
            "integrity": "sha512-a"
          },
          "node_modules/user": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/user/-/user-1.0.0.tgz",
            "integrity": "sha512-u",
            "dependencies": { "my-a": "npm:a@^1.0.0" }
          }
        }
      }),
      &["npm:a@^1.0.0", "npm:user@^1.0.0"],
      serde_json::json!({
        "version": "5",
        "specifiers": {
          "npm:a@^1.0.0": "1.1.0",
          "npm:user@^1.0.0": "1.0.0"
        },
        "npm": {
          "a@1.1.0": {
            "integrity": "sha512-a"
          },
          "user@1.0.0": {
            "integrity": "sha512-u",
            "dependencies": ["my-a@npm:a@1.1.0"]
          }
        }
      }),
    );
  }

  #[test]
  fn optional_deps_with_os_and_cpu() {
    assert_translates(
      serde_json::json!({
        "lockfileVersion": 3,
        "packages": {
          "": {
            "dependencies": { "main": "1.0.0" }
          },
          "node_modules/main": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/main/-/main-1.0.0.tgz",
            "integrity": "sha512-m",
            "optionalDependencies": { "arch": "1.0.0" }
          },
          "node_modules/arch": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/arch/-/arch-1.0.0.tgz",
            "integrity": "sha512-a",
            "os": ["win32"],
            "cpu": ["x64", "arm64"],
            "hasInstallScript": true,
            "bin": { "arch": "bin.js" },
            "optional": true
          }
        }
      }),
      &["npm:main@1.0.0"],
      serde_json::json!({
        "version": "5",
        "specifiers": {
          "npm:main@1.0.0": "1.0.0"
        },
        "npm": {
          "arch@1.0.0": {
            "integrity": "sha512-a",
            "os": ["win32"],
            "cpu": ["x64", "arm64"],
            "scripts": true,
            "bin": true
          },
          "main@1.0.0": {
            "integrity": "sha512-m",
            "optionalDependencies": ["arch"]
          }
        }
      }),
    );
  }

  #[test]
  fn peer_deps() {
    assert_translates(
      serde_json::json!({
        "lockfileVersion": 3,
        "packages": {
          "": {
            "dependencies": { "consumer": "^1.0.0" }
          },
          "node_modules/consumer": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/consumer/-/consumer-1.0.0.tgz",
            "integrity": "sha512-c",
            "peerDependencies": {
              "peer": "1",
              "opt-peer": "1",
              "missing-opt-peer": "1"
            },
            "peerDependenciesMeta": {
              "opt-peer": { "optional": true },
              "missing-opt-peer": { "optional": true }
            }
          },
          "node_modules/peer": {
            "version": "1.2.0",
            "resolved": "https://registry.npmjs.org/peer/-/peer-1.2.0.tgz",
            "integrity": "sha512-p"
          },
          "node_modules/opt-peer": {
            "version": "1.3.0",
            "resolved": "https://registry.npmjs.org/opt-peer/-/opt-peer-1.3.0.tgz",
            "integrity": "sha512-o"
          }
        }
      }),
      &["npm:consumer@^1.0.0"],
      serde_json::json!({
        "version": "5",
        "specifiers": {
          "npm:consumer@^1.0.0": "1.0.0"
        },
        "npm": {
          "consumer@1.0.0": {
            "integrity": "sha512-c",
            "dependencies": ["opt-peer", "peer"],
            "optionalPeers": ["opt-peer"]
          },
          "opt-peer@1.3.0": {
            "integrity": "sha512-o"
          },
          "peer@1.2.0": {
            "integrity": "sha512-p"
          }
        }
      }),
    );
  }

  #[test]
  fn skips_non_registry_deps_and_their_dependents() {
    assert_translates(
      serde_json::json!({
        "lockfileVersion": 3,
        "packages": {
          "": {
            "dependencies": {
              "a": "^1.0.0",
              "g": "git+https://github.com/user/repo.git",
              "t": "https://example.com/t.tgz",
              "f": "file:../f",
              "uses-git": "^1.0.0"
            }
          },
          "node_modules/a": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/a/-/a-1.0.0.tgz",
            "integrity": "sha512-a"
          },
          "node_modules/g": {
            "version": "1.0.0",
            "resolved": "git+ssh://git@github.com/user/repo.git#abcdef"
          },
          "node_modules/t": {
            "version": "1.0.0",
            "resolved": "https://example.com/t.tgz",
            "integrity": "sha512-t"
          },
          "node_modules/f": {
            "resolved": "../f",
            "link": true
          },
          "node_modules/uses-git": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/uses-git/-/uses-git-1.0.0.tgz",
            "integrity": "sha512-ug",
            "dependencies": { "g": "git+https://github.com/user/repo.git" }
          }
        }
      }),
      &["npm:a@^1.0.0", "npm:uses-git@^1.0.0"],
      serde_json::json!({
        "version": "5",
        "specifiers": {
          "npm:a@^1.0.0": "1.0.0"
        },
        "npm": {
          "a@1.0.0": {
            "integrity": "sha512-a"
          }
        }
      }),
    );
  }

  #[test]
  fn skips_workspace_members_and_links() {
    assert_translates(
      serde_json::json!({
        "lockfileVersion": 2,
        "packages": {
          "": {
            "workspaces": ["packages/*"]
          },
          // a workspace member is an importer: its dependencies become
          // top level requirements, but the member itself is not seeded
          "packages/member": {
            "name": "member",
            "version": "1.0.0",
            "dependencies": { "a": "^1.0.0", "b": "^1.0.0" }
          },
          "node_modules/member": {
            "resolved": "packages/member",
            "link": true
          },
          "node_modules/a": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/a/-/a-1.0.0.tgz",
            "integrity": "sha512-a"
          },
          // b's regular dependency resolves to the workspace link, which
          // can't be translated, so b is dropped entirely
          "node_modules/b": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/b/-/b-1.0.0.tgz",
            "integrity": "sha512-b",
            "dependencies": { "member": "^1.0.0" }
          }
        }
      }),
      &["npm:a@^1.0.0", "npm:b@^1.0.0"],
      serde_json::json!({
        "version": "5",
        "specifiers": {
          "npm:a@^1.0.0": "1.0.0"
        },
        "npm": {
          "a@1.0.0": {
            "integrity": "sha512-a"
          }
        }
      }),
    );
  }

  #[test]
  fn drops_deps_not_tracked_by_the_workspace() {
    assert_translates(
      serde_json::json!({
        "lockfileVersion": 3,
        "packages": {
          "": {
            "dependencies": { "a": "^1.0.0", "stale": "^1.0.0" }
          },
          "node_modules/a": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/a/-/a-1.0.0.tgz",
            "integrity": "sha512-a"
          },
          "node_modules/stale": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/stale/-/stale-1.0.0.tgz",
            "integrity": "sha512-s",
            "dependencies": { "stale-child": "^1.0.0" }
          },
          "node_modules/stale-child": {
            "version": "1.0.0",
            "resolved": "https://registry.npmjs.org/stale-child/-/stale-child-1.0.0.tgz",
            "integrity": "sha512-sc"
          }
        }
      }),
      &["npm:a@^1.0.0"],
      serde_json::json!({
        "version": "5",
        "specifiers": {
          "npm:a@^1.0.0": "1.0.0"
        },
        "npm": {
          "a@1.0.0": {
            "integrity": "sha512-a"
          }
        }
      }),
    );
  }

  #[test]
  fn version_mismatch_is_not_seeded() {
    // the package.json requirement no longer matches what the npm
    // lockfile pinned, so nothing gets seeded for it
    assert_translates(
      serde_json::json!({
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
      }),
      &["npm:a@^2.0.0"],
      serde_json::json!({
        "version": "5"
      }),
    );
  }

  #[test]
  fn unsupported_lockfile_version() {
    let err = translate(
      serde_json::json!({
        "lockfileVersion": 1,
        "dependencies": {}
      }),
      &[],
    )
    .unwrap_err();
    assert!(err.to_string().contains("unsupported lockfileVersion: 1"));
  }

  #[test]
  fn invalid_json() {
    assert!(
      lockfile_content_from_npm_package_lock_json(
        "not json",
        &Default::default()
      )
      .is_err()
    );
  }
}
