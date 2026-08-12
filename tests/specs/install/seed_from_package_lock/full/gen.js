// generates a package-lock.json with the real integrity hashes from the
// test registry, pinning @denotest/has-patch-versions to 0.1.0 (an older
// version than what ^0.1.0 would resolve to)
async function packageDist(name, version) {
  const response = await fetch(`http://localhost:4260/${name}`);
  const packument = await response.json();
  return packument.versions[version].dist;
}

const esmBasic = await packageDist("@denotest/esm-basic", "1.0.0");
const hasPatch = await packageDist("@denotest/has-patch-versions", "0.1.0");

Deno.writeTextFileSync(
  "package-lock.json",
  JSON.stringify(
    {
      name: "full",
      version: "1.0.0",
      lockfileVersion: 3,
      requires: true,
      packages: {
        "": {
          name: "full",
          version: "1.0.0",
          dependencies: {
            "@denotest/esm-basic": "^1.0.0",
            "@denotest/has-patch-versions": "^0.1.0",
          },
        },
        "node_modules/@denotest/esm-basic": {
          version: "1.0.0",
          resolved: esmBasic.tarball,
          integrity: esmBasic.integrity,
        },
        "node_modules/@denotest/has-patch-versions": {
          version: "0.1.0",
          resolved: hasPatch.tarball,
          integrity: hasPatch.integrity,
        },
      },
    },
    null,
    2,
  ) + "\n",
);
console.log("generated package-lock.json");
