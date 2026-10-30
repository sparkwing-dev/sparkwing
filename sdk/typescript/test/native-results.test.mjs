import assert from "node:assert/strict";
import { test } from "node:test";
import { spawnSync } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { passedNativeTests } from "../native/verification.mjs";

const names = ["TestNativeProtocol", "TestNativeConformance"];
const events = (action) => names.map((Test) => JSON.stringify({ Action: action, Test })).join("\n");

test("native release verification requires both executed tests and a successful package", () => {
  for (const output of [
    "",
    events("skip"),
    events("pass"),
    JSON.stringify({ Action: "pass" }),
    events("pass") + '\n{"Action":"fail"}',
    "not JSON",
  ]) {
    assert.throws(() => passedNativeTests(output), /native|JSON/);
  }
  assert.deepEqual(passedNativeTests(events("pass") + '\n{"Action":"pass"}'), names);
  assert.throws(
    () => passedNativeTests(events("pass") + '\n{"Action":"skip","Test":"TestNativeProtocol/child"}\n{"Action":"pass"}'),
    /native/,
  );
});

test("native release refuses a nonnative bridge target before changing package versions", () => {
  const manifest = new URL("../package.json", import.meta.url);
  const before = readFileSync(manifest, "utf8");
  const result = spawnSync(
    process.execPath,
    [
      fileURLToPath(new URL("../native/release.mjs", import.meta.url)),
      "bridge",
      "--version",
      "0.71.0",
      "--arch",
      process.arch === "x64" ? "arm64" : "x64",
    ],
    { encoding: "utf8" },
  );
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /native Windows Node/);
  assert.equal(readFileSync(manifest, "utf8"), before);
});

test("native release prints captured Go test failures", (t) => {
  const root = mkdtempSync(join(tmpdir(), "sdk-release-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  mkdirSync(join(root, "native"));
  for (const file of ["release.mjs", "verification.mjs"])
    copyFileSync(new URL(`../native/${file}`, import.meta.url), join(root, "native", file));
  const output = JSON.stringify({ Action: "output", Test: "TestNativeProtocol", Output: "bridge fixture failed\n" }) + "\n";
  const fixture = join(root, "fixture.mjs");
  writeFileSync(fixture, `
    import childProcess from "node:child_process";
    import { syncBuiltinESMExports } from "node:module";
    import { pathToFileURL } from "node:url";
    Object.defineProperty(process, "platform", { value: "win32" });
    Object.defineProperty(process, "arch", { value: "x64" });
    childProcess.spawnSync = (command, args) => {
      if (command === "go" && args[0] === "env")
        return { status: 0, stdout: JSON.stringify({ GOOS: "windows", GOARCH: "amd64" }) };
      if (command === "go" && args[0] === "test")
        return { status: 1, stdout: ${JSON.stringify(output)} };
      return { status: 0, stdout: "" };
    };
    syncBuiltinESMExports();
    await import(pathToFileURL(${JSON.stringify(join(root, "native/release.mjs"))}).href);
  `);
  const result = spawnSync(process.execPath, [fixture,
    "bridge", "--version", "0.71.0", "--arch", "x64", "--headers", ".", "--node-lib", ".",
  ], { encoding: "utf8" });
  assert.equal(result.status, 1, result.stderr);
  assert.ok(result.stderr.includes(output), result.stderr);
  assert.match(result.stderr, /go failed \(1\)/);
});
