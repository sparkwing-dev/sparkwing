import { spawnSync } from "node:child_process";
import { readFile, rm, writeFile } from "node:fs/promises";
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
if (positionals.length !== 1 || !["bridge", "pack"].includes(operation)) throw new Error("expected bridge or pack");
const version = values.version?.replace(/^v/, "");
if (!version || version === "0.0.0" || !/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$/.test(version))
  throw new Error("--version requires the engine release version");
const root = dirname(dirname(fileURLToPath(import.meta.url)));
const run = (command, args, capture = false) => {
  const result = spawnSync(command, args, {
    cwd: root,
    encoding: "utf8",
    stdio: capture ? ["ignore", "pipe", "inherit"] : "inherit",
    env: command === "go" ? { ...process.env, GOWORK: "off" } : process.env,
  });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`${command} failed (${result.status ?? result.signal})`);
  return result.stdout;
};
const npm = (args) =>
  process.platform === "win32"
    ? run(process.execPath, [process.env.npm_execpath || join(dirname(process.execPath), "node_modules/npm/bin/npm-cli.js"), ...args])
    : run("npm", args);

if (operation === "bridge") {
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
