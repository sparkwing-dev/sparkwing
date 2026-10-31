import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { resolve } from "node:path";

const [archive, version, tag] = process.argv.slice(2);
if (process.argv.length !== 5 || !archive || !/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$/.test(version) || !tag) {
  throw new Error("usage: publish-typescript.mjs <archive> <engine-version> <npm-tag>");
}
if (!version.startsWith("0.")) throw new Error("SDK publication is restricted to major version 0");
const manifest = spawnSync("tar", ["-xOzf", archive, "package/package.json"], { encoding: "utf8" });
if (manifest.error) throw manifest.error;
if (manifest.status !== 0) throw new Error("cannot read packed package.json");
const packed = JSON.parse(manifest.stdout);
if (packed.name !== "@sparkwing/sdk") throw new Error("packed package name must be @sparkwing/sdk");
// npm provenance rejects the upload unless repository.url names the building repository.
if (packed.repository?.url !== "git+https://github.com/sparkwing-dev/sparkwing.git") {
  throw new Error("packed package repository must be git+https://github.com/sparkwing-dev/sparkwing.git");
}
if (packed.version !== version) throw new Error("packed package version must equal the release tag without v");
if (version.includes("-") && tag !== "next") throw new Error("prereleases must use the next npm tag");

function npmView(args) {
  const result = spawnSync("npm", ["view", ...args, "--json"], { encoding: "utf8" });
  if (result.error) throw result.error;
  let metadata;
  try {
    metadata = JSON.parse(result.stdout);
  } catch {
    throw new Error("npm package lookup returned no valid JSON result");
  }
  if (result.status === 0) return metadata;
  if (result.status !== null && metadata?.error?.code === "E404") return undefined;
  throw new Error("npm package lookup failed; publication stopped");
}

const integrity = "sha512-" + createHash("sha512").update(readFileSync(archive)).digest("base64");
const metadata = npmView([`@sparkwing/sdk@${version}`, "dist.integrity"]);
if (metadata !== undefined) {
  if (metadata !== integrity) throw new Error("published SDK bytes differ from the assembled package");
} else {
  if (tag === "latest") {
    const lookup = npmView(["@sparkwing/sdk", "versions"]);
    const versions = lookup === undefined ? [] : lookup;
    if (!Array.isArray(versions) || versions.some(value => typeof value !== "string")) {
      throw new Error("npm versions lookup returned no valid version list");
    }
    const candidate = version.split(".").map(BigInt);
    const newer = versions.some(value => {
      if (!/^0\.\d+\.\d+$/.test(value)) return false;
      const published = value.split(".").map(BigInt);
      for (let i = 0; i < candidate.length; i++) {
        if (candidate[i] !== published[i]) return candidate[i] < published[i];
      }
      return true;
    });
    if (newer) throw new Error("latest requires a stable version newer than every published stable 0.x version");
  }
  // npm reads a relative "dir/file.tgz" as a GitHub owner/repo shorthand.
  const args = ["publish", resolve(archive), "--access", "public", "--tag", tag, "--ignore-scripts", "--provenance", "--json"];
  for (const dryRun of [true, false]) {
    const published = spawnSync("npm", [...args, ...(dryRun ? ["--dry-run"] : [])], { stdio: "inherit" });
    if (published.error) throw published.error;
    if (published.status !== 0) throw new Error(`npm ${dryRun ? "dry run" : "publication"} failed (${published.status ?? published.signal})`);
  }
}
