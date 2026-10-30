import { spawnSync } from "node:child_process";
import { cp, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import { digest, passedNativeTests, sourceDigest } from "./verification.mjs";

const { values, positionals } = parseArgs({
  allowPositionals: true,
  options: {
    version: { type: "string" },
    headers: { type: "string" },
    "node-lib": { type: "string" },
    arch: { type: "string" },
    destination: { type: "string" },
  },
});
const [operation] = positionals;
if (positionals.length !== 1 || !["bridge", "pack", "local-pack"].includes(operation)) throw new Error("expected bridge, pack or local-pack");
const version = values.version?.replace(/^v/, "");
if (!version || version === "0.0.0" || !/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$/.test(version))
  throw new Error("--version requires the engine release version");
const root = dirname(dirname(fileURLToPath(import.meta.url)));
const run = (command, args, capture = false, cwd = root) => {
  const result = spawnSync(command, args, {
    cwd,
    encoding: "utf8",
    stdio: capture ? ["ignore", "pipe", "inherit"] : "inherit",
    env: command === "go" ? { ...process.env, GOWORK: "off" } : process.env,
  });
  if (result.error || result.status !== 0) {
    if (capture && result.stdout) process.stderr.write(result.stdout);
    if (result.error) throw result.error;
    throw new Error(`${command} failed (${result.status ?? result.signal})`);
  }
  return result.stdout;
};
const npm = (args, cwd = root) =>
  process.platform === "win32"
    ? run(process.execPath, [process.env.npm_execpath || join(dirname(process.execPath), "node_modules/npm/bin/npm-cli.js"), ...args], false, cwd)
    : run("npm", args, false, cwd);

if (operation === "local-pack") {
  if (process.platform === "win32") throw new Error("local-pack supports Linux and macOS; Windows requires verified native bridges");
  npm(["run", "build"]);
  const staging = await mkdtemp(join(tmpdir(), "sparkwing-sdk-pack-"));
  try {
    const manifest = JSON.parse(await readFile(join(root, "package.json"), "utf8"));
    manifest.version = version;
    manifest.private = true;
    delete manifest.scripts;
    manifest.files = ["dist", "index.d.ts", "README.md"];
    await writeFile(join(staging, "package.json"), JSON.stringify(manifest) + "\n");
    for (const path of manifest.files) await cp(join(root, path), join(staging, path), { recursive: true });
    npm(["pack", "--ignore-scripts", "--pack-destination", resolve(values.destination ?? root)], staging);
  } finally {
    await rm(staging, { recursive: true, force: true });
  }
} else if (operation === "bridge") {
  const arch = values.arch;
  if (process.platform !== "win32" || !["x64", "arm64"].includes(arch) || arch !== process.arch)
    throw new Error("bridge verification requires native Windows Node for the target architecture");
  if (!values.headers || !values["node-lib"]) throw new Error("--headers and --node-lib are required");
  const target = JSON.parse(run("go", ["env", "-json", "GOOS", "GOARCH"], true));
  if (target.GOOS !== "windows" || target.GOARCH !== (arch === "x64" ? "amd64" : "arm64"))
    throw new Error("bridge verification requires native Windows Go for the target architecture");
  npm(["version", version, "--no-git-tag-version", "--allow-same-version", "--ignore-scripts"]);
  const directory = join(root, "native", `win32-${arch}`);
  await rm(join(directory, "verification.json"), { force: true });
  run(process.execPath, [
    join(root, "native/build.mjs"),
    "--headers",
    resolve(values.headers),
    "--node-lib",
    resolve(values["node-lib"]),
    "--arch",
    arch,
  ]);
  const tests = passedNativeTests(
    run("go", ["test", "-json", "-p", "4", "-count=1", "./test/nativehost", "-run", "^TestNative(Protocol|Conformance)$"], true),
  );
  const receipt = {
    platform: "win32",
    arch,
    version,
    node: process.version,
    tests,
    source: await sourceDigest(root),
    sha256: digest(await readFile(join(directory, "protocol.node"))),
  };
  await writeFile(join(directory, "verification.json"), JSON.stringify(receipt) + "\n");
} else {
  npm(["version", version, "--no-git-tag-version", "--allow-same-version", "--ignore-scripts"]);
  run(process.execPath, [join(root, "native/check-package.mjs")]);
  npm(["pack", ...(values.destination ? ["--pack-destination", resolve(values.destination)] : [])]);
}
