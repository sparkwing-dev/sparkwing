import { execFileSync } from "node:child_process";
import { writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

const sdkModule = "github.com/sparkwing-dev/sparkwing/sparkwing";

export function goSDKEntry(root) {
  const manifest = JSON.parse(execFileSync("go", ["mod", "edit", "-json"], {
    cwd: root, encoding: "utf8", env: { ...process.env, GOWORK: "off", GOFLAGS: "-trimpath" },
  }));
  const requirement = manifest.Require?.find(entry => entry.Path === sdkModule);
  if (!requirement) return [];
  const version = requirement.Version;
  if (!/^v0\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.test(version))
    throw new Error("engine must require a stable v0 Go SDK version");
  const ref = `refs/tags/sparkwing/${version}`;
  const git = (...args) => execFileSync("git", ["-C", root, ...args], { encoding: "utf8" });
  git("fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "origin", `${ref}:${ref}`);
  const message = git("for-each-ref", "--format=%(contents)", ref);
  const records = [...message.matchAll(/^SDK-Source-SHA256: ([0-9a-f]{64})$/gm)];
  if (records.length !== 1) throw new Error(`SDK tag ${ref} must record exactly one source digest`);
  const source = git("show", `${ref}:sparkwing/protocol/protocol_contract.go`);
  const declaration = source.match(/^const Version = ([1-9][0-9]*)\s*$/m);
  if (!declaration) throw new Error(`SDK tag ${ref} has no literal protocol Version declaration`);
  const protocol = Number(declaration[1]);
  if (!Number.isSafeInteger(protocol)) throw new Error("SDK protocol version is out of range");
  return [{ language: "go", version, protocol, source_digest: records[0][1] }];
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [root, output] = process.argv.slice(2);
  if (!root || !output) throw new Error("usage: go-sdk-release-entry.mjs <selected-source-root> <entry.json>");
  writeFileSync(output, JSON.stringify(goSDKEntry(resolve(root)), null, 2) + "\n");
}
