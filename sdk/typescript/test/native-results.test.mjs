import assert from "node:assert/strict";
import { test } from "node:test";
import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
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
