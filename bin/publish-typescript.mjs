import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { spawnSync } from "node:child_process";

const [archive, version, tag] = process.argv.slice(2);
if (process.argv.length !== 5 || !archive || !/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(version) || !tag) {
  throw new Error("usage: publish-typescript.mjs <archive> <engine-version> <npm-tag>");
}
const integrity = "sha512-" + createHash("sha512").update(readFileSync(archive)).digest("base64");
const existing = spawnSync("npm", ["view", `@sparkwing/sdk@${version}`, "dist.integrity", "--json"], { encoding: "utf8" });
if (existing.error) throw existing.error;
let metadata;
try {
  metadata = JSON.parse(existing.stdout);
} catch {
  throw new Error("npm package lookup returned no valid JSON result");
}
if (existing.status === 0) {
  if (metadata !== integrity) throw new Error("published SDK bytes differ from the assembled package");
} else {
  if (existing.status === null || metadata?.error?.code !== "E404") {
    throw new Error("npm package lookup failed; publication stopped");
  }
  const published = spawnSync("npm", ["publish", archive, "--access", "public", "--tag", tag, "--ignore-scripts"], { stdio: "inherit" });
  if (published.error) throw published.error;
  if (published.status !== 0) throw new Error(`npm publication failed (${published.status ?? published.signal})`);
}
