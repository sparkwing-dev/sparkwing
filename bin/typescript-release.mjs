import { createHash } from "node:crypto";
import { readFileSync, appendFileSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

const packageInputs = new Set(["package.json", "package-lock.json", "README.md", "index.d.ts", "tsconfig.json", "tsconfig.build.json"]);

export function sdkDigest(root, tools = root) {
  const paths = execFileSync("git", ["-C", root, "ls-files", "-z", "--", "sdk/typescript"], { encoding: "utf8" })
    .split("\0").filter(Boolean).filter(path => {
      const input = path.slice("sdk/typescript/".length);
      // safety: Windows publication runs nativehost and binds its entire fixture tree into verification receipts.
      return packageInputs.has(input) || input.startsWith("src/") || input.startsWith("test/nativehost/") ||
        input.startsWith("native/") && !/^native\/win32-(x64|arm64)\//.test(input);
    }).sort();
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
  const workflow = readFileSync(resolve(tools, ".github/workflows/release.yaml"), "utf8");
  const start = workflow.indexOf("  typescript-native:"), end = workflow.indexOf("\n  release:", start);
  if (start < 0 || end < start) throw new Error("publication workflow has no TypeScript native build jobs");
  const jobs = workflow.slice(start, end);
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

export function decideRelease(version, digest, entries, { recovery = false, engineVersion = "" } = {}) {
  const candidates = entries.filter(entry => entry.language === "typescript");
  const latest = candidates.sort((a, b) => compareVersions(b.version, a.version))[0];
  if (latest?.source_digest === digest && !latest.revoked) return { publish: false, entry: latest };
  const existing = candidates.find(entry => entry.version === `v${version}`);
  if (recovery && existing?.source_digest === digest && !existing.revoked) return { publish: false, entry: existing };
  if (!/^0\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$/.test(version) || version === "0.0.0")
    throw new Error("package.json must select an independent SDK version in major 0");
  if (existing && existing.source_digest !== digest)
    throw new Error("SDK version already names different source; bump sdk/typescript/package.json");
  if (latest && compareVersions(`v${version}`, latest.version) <= 0)
    throw new Error("changed or revoked SDK requires a version newer than the latest SDK entry");
  if (engineVersion.includes("-"))
    throw new Error("changed SDK source must publish with a stable engine release before an engine prerelease can use it");
  return { publish: true, entry: { language: "typescript", version: `v${version}`, protocol: 2, source_digest: digest } };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [root, policy, output, tools = root, engineVersion = ""] = process.argv.slice(2);
  if (!root || !policy || !output) throw new Error("usage: typescript-release.mjs <source-root> <verified-policy.json> <entry.json> [publication-tools-root] [engine-version]");
  const manifest = JSON.parse(readFileSync(resolve(root, "sdk/typescript/package.json")));
  const index = JSON.parse(readFileSync(policy));
  const decision = decideRelease(manifest.version, sdkDigest(root, tools), index.sdks ?? [], { recovery: process.env.GITHUB_EVENT_NAME === "workflow_dispatch", engineVersion });
  writeFileSync(output, JSON.stringify([decision.entry]) + "\n");
  if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT,
    `publish=${decision.publish}\nversion=${decision.entry.version.slice(1)}\n`);
  console.log(JSON.stringify(decision));
}
