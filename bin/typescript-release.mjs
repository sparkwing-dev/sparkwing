import { createHash } from "node:crypto";
import { readFileSync, appendFileSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

export function sdkDigest(root) {
  const paths = execFileSync("git", ["-C", root, "ls-files", "-z", "--", "sdk/typescript"], { encoding: "utf8" })
    .split("\0").filter(Boolean).filter(path => !/\/(node_modules|dist|win32-x64|win32-arm64)\//.test(path)).sort();
  const hash = createHash("sha256");
  for (const path of paths) {
    let bytes = readFileSync(resolve(root, path));
    if (path.endsWith("/package.json") || path.endsWith("/package-lock.json")) {
      const manifest = JSON.parse(bytes);
      delete manifest.version;
      if (manifest.packages?.[""]) delete manifest.packages[""].version;
      bytes = Buffer.from(JSON.stringify(manifest));
    }
    hash.update(path + "\0" + bytes.length + "\0").update(bytes);
  }
  const workflow = readFileSync(resolve(root, ".github/workflows/release.yaml"), "utf8");
  const jobs = workflow.slice(workflow.indexOf("  typescript-native:"), workflow.indexOf("\n  release:"));
  hash.update("native-build-inputs\0").update(jobs);
  return hash.digest("hex");
}

function compareVersions(a, b) {
  const [left, leftPre] = a.replace(/^v/, "").split(/-(.*)/);
  const [right, rightPre] = b.replace(/^v/, "").split(/-(.*)/);
  const parts = left.split(".").map(BigInt), other = right.split(".").map(BigInt);
  for (let i = 0; i < 3; i++) if (parts[i] !== other[i]) return parts[i] > other[i] ? 1 : -1;
  if (leftPre === rightPre) return 0;
  if (!leftPre) return 1;
  if (!rightPre) return -1;
  const ids = leftPre.split("."), others = rightPre.split(".");
  for (let i = 0; i < Math.max(ids.length, others.length); i++) {
    if (ids[i] === others[i]) continue;
    if (ids[i] === undefined) return -1;
    if (others[i] === undefined) return 1;
    const numeric = /^\d+$/.test(ids[i]), otherNumeric = /^\d+$/.test(others[i]);
    if (numeric && otherNumeric) return BigInt(ids[i]) > BigInt(others[i]) ? 1 : -1;
    if (numeric !== otherNumeric) return numeric ? -1 : 1;
    return ids[i] > others[i] ? 1 : -1;
  }
  return 0;
}

export function decideRelease(version, digest, entries) {
  if (!/^0\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$/.test(version) || version === "0.0.0")
    throw new Error("package.json must select an independent SDK version in major 0");
  const candidates = entries.filter(entry => entry.language === "typescript");
  const existing = candidates.find(entry => entry.version === `v${version}`);
  if (existing && existing.source_digest !== digest)
    throw new Error("SDK version already names different source; bump sdk/typescript/package.json");
  const latest = candidates.sort((a, b) => compareVersions(b.version, a.version))[0];
  if (latest?.source_digest === digest && !latest.revoked) return { publish: false, entry: latest };
  if (latest && compareVersions(`v${version}`, latest.version) <= 0)
    throw new Error("changed or revoked SDK requires a version newer than the latest SDK entry");
  return { publish: true, entry: { language: "typescript", version: `v${version}`, protocol: 2, source_digest: digest } };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [root, policy, output] = process.argv.slice(2);
  if (!root || !policy || !output) throw new Error("usage: typescript-release.mjs <source-root> <verified-policy.json> <entry.json>");
  const manifest = JSON.parse(readFileSync(resolve(root, "sdk/typescript/package.json")));
  const index = JSON.parse(readFileSync(policy));
  const decision = decideRelease(manifest.version, sdkDigest(root), index.sdks ?? []);
  writeFileSync(output, JSON.stringify([decision.entry]) + "\n");
  if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT,
    `publish=${decision.publish}\nversion=${decision.entry.version.slice(1)}\n`);
  console.log(JSON.stringify(decision));
}
