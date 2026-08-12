// verifies the versions and integrity hashes pinned in package-lock.json
// were preserved in the created deno.lock
const packageLock = JSON.parse(Deno.readTextFileSync("package-lock.json"));
const denoLock = JSON.parse(Deno.readTextFileSync("deno.lock"));

console.log(
  denoLock.specifiers["npm:@denotest/has-patch-versions@0.1"],
);

const integritiesMatch = [
  ["@denotest/esm-basic", "1.0.0"],
  ["@denotest/has-patch-versions", "0.1.0"],
].every(([name, version]) =>
  packageLock.packages[`node_modules/${name}`].integrity ===
    denoLock.npm[`${name}@${version}`].integrity
);
console.log(integritiesMatch ? "integrities match" : "integrities differ");
