#!/usr/bin/env node
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";

const [action, directory, before] = process.argv.slice(2);
if (!["inputs", "check", "record"].includes(action) || !directory) {
  console.error("usage: web-build-proof.mjs inputs|check|record <root> [input-digest]");
  process.exit(2);
}
const root = path.resolve(directory);
const web = path.join(root, "web");
const output = path.join(root, "internal/web/next-out");
const receipt = path.join(root, "internal/web/.build-state/receipt.json");
const generatedDirectories = new Set([
  "node_modules", ".next", "out", "build", "coverage", "playwright-report", "test-results", ".git",
]);
const environmentNames = new Set([
  "NODE_ENV", "NODE_OPTIONS", "NODE_PATH", "SPARKWING_API_URL",
  "SPARKWING_CONTROLLER_URL", "SPARKWING_LOGS_URL", "TZ", "LANG", "LC_ALL",
]);

function add(hash, label, value) {
  const bytes = Buffer.isBuffer(value) ? value : Buffer.from(value);
  hash.update(JSON.stringify([label, bytes.length]));
  hash.update(bytes);
}

function tree(hash, directory, source, relative = "") {
  const entries = fs.readdirSync(path.join(directory, relative), { withFileTypes: true });
  entries.sort((a, b) => a.name.localeCompare(b.name, "en"));
  for (const entry of entries) {
    const name = path.posix.join(relative, entry.name);
    if (source && ((relative === "" && generatedDirectories.has(entry.name)) || entry.name.endsWith(".tsbuildinfo") ||
      name === "next-env.d.ts" || /^(npm|yarn|pnpm)-debug\.log/.test(entry.name) || entry.name === ".DS_Store")) continue;
    const full = path.join(directory, name);
    if (entry.isDirectory()) {
      add(hash, name, "directory");
      tree(hash, directory, source, name);
      continue;
    }
    if (entry.isSymbolicLink()) {
      if (!source || !fs.statSync(full).isFile()) throw new Error("unsupported link");
      add(hash, name + ":link", fs.readlinkSync(full));
    } else if (!entry.isFile()) {
      throw new Error("unsupported file");
    }
    if (source) add(hash, name + ":executable", String(fs.statSync(full).mode & 0o111));
    const bytes = fs.readFileSync(full);
    add(hash, name, bytes);
    if (source && entry.name.startsWith(".env")) {
      const text = bytes.toString("utf8");
      for (const match of text.matchAll(/^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=/gm)) environmentNames.add(match[1]);
      for (const match of text.matchAll(/\$\{?([A-Za-z_][A-Za-z0-9_]*)/g)) environmentNames.add(match[1]);
    }
  }
}

function inputs() {
  if (process.env.NODE_OPTIONS || process.env.NODE_PATH || Object.entries(process.env).some(
    ([name, value]) => /^npm_config_node_options$/i.test(name) && value,
  )) throw new Error("external Node configuration is not reusable");
  const hash = createHash("sha256");
  add(hash, "format", "1");
  tree(hash, web, true);
  for (const name of ["build-web.sh", "web-build-lock.sh", "web-build-proof.mjs"]) {
    add(hash, name, fs.readFileSync(path.join(root, "bin", name)));
  }
  add(hash, "node", JSON.stringify([process.version, process.platform, process.arch]));
  const npm = execFileSync("npm", ["--version"], {
    cwd: web, encoding: "utf8", timeout: 10000, stdio: ["ignore", "pipe", "pipe"],
  }).trim();
  if (!npm) throw new Error("npm version missing");
  add(hash, "npm", npm);
  const config = JSON.parse(execFileSync("npm", ["config", "list", "--json"], {
    cwd: web, encoding: "utf8", timeout: 10000, stdio: ["ignore", "pipe", "pipe"],
  }));
  if (!config || typeof config !== "object" || Array.isArray(config)) throw new Error("npm config unavailable");
  if (config["node-options"] || config["script-shell"]) throw new Error("external npm hooks are not reusable");
  add(hash, "npm-config", JSON.stringify(Object.entries(config).sort(([a], [b]) => a.localeCompare(b, "en"))));
  for (const name of Object.keys(process.env)) {
    if (name.startsWith("NEXT_") || /^npm_config_/i.test(name)) environmentNames.add(name);
  }
  for (const name of [...environmentNames].sort()) {
    add(hash, name, JSON.stringify(process.env[name] ?? null));
  }
  return hash.digest("hex");
}

function outputDigest() {
  if (!fs.lstatSync(path.join(output, "index.html")).isFile()) throw new Error("missing index");
  const hash = createHash("sha256");
  tree(hash, output, false);
  return hash.digest("hex");
}

try {
  const input = inputs();
  if (action === "inputs") {
    console.log(input);
  } else if (action === "check") {
    const saved = JSON.parse(fs.readFileSync(receipt, "utf8"));
    if (saved.version !== 1 || saved.input !== input || saved.output !== outputDigest()) process.exit(1);
  } else {
    if (!before || before !== input) process.exit(1);
    const proof = { version: 1, input, output: outputDigest() };
    const temporary = `${receipt}.${process.pid}.tmp`;
    try {
      fs.writeFileSync(temporary, JSON.stringify(proof) + "\n", { mode: 0o600, flag: "wx" });
      fs.renameSync(temporary, receipt);
    } finally {
      try { fs.unlinkSync(temporary); } catch (error) { if (error.code !== "ENOENT") throw error; }
    }
  }
} catch {
  // safety: dotenv and npm diagnostics may contain credentials; report only proof availability.
  if (action !== "check") console.error("web build proof unavailable; output will not be reused");
  process.exit(1);
}
