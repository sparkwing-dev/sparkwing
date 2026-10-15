import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, rmSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";

const { values } = parseArgs({ options: {
  headers: { type: "string" }, "node-lib": { type: "string" }, arch: { type: "string", default: process.arch }, help: { type: "boolean" },
} });
if (values.help) {
  console.log("Run in an MSVC developer shell for the target architecture:\nnode native/build.mjs --headers <Node include/node> --node-lib <target node.lib> --arch <x64|arm64>");
  process.exit(0);
}
if (process.platform !== "win32") throw new Error("the protocol bridge builds with MSVC on Windows");
if (!values.headers || !values["node-lib"]) throw new Error("--headers and --node-lib are required");
if (values.arch !== "x64" && values.arch !== "arm64") throw new Error("--arch must be x64 or arm64");
const headers = resolve(values.headers);
const nodeLib = resolve(values["node-lib"]);
if (!existsSync(join(headers, "node_api.h")) || !existsSync(join(headers, "uv.h")) || !existsSync(nodeLib)) {
  throw new Error("the supplied Node headers or import library are missing");
}
const native = dirname(fileURLToPath(import.meta.url));
const output = join(native, `win32-${values.arch}`);
const object = join(output, "protocol.obj");
mkdirSync(output, { recursive: true });
const result = spawnSync("cl.exe", [
  "/nologo", "/LD", "/MT", "/O2", "/W4", "/WX", "/std:c11", "/DNAPI_VERSION=8", "/DBUILDING_NODE_EXTENSION", "/DUSING_UV_SHARED=1", `/I${headers}`,
  join(native, "protocol.c"), `/Fo${object}`, "/link", `/MACHINE:${values.arch.toUpperCase()}`,
  `/OUT:${join(output, "protocol.node")}`, `/IMPLIB:${join(output, "protocol.lib")}`, nodeLib,
], { stdio: "inherit", cwd: native });
if (result.error) throw result.error;
if (result.status !== 0) throw new Error(`protocol bridge compilation failed (${result.status ?? result.signal})`);
for (const path of [object, join(output, "protocol.lib"), join(output, "protocol.exp")]) rmSync(path, { force: true });
