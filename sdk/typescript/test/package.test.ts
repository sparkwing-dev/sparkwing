import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import * as sdk from "../src/index.ts";

test("public root exposes the declared SDK without runtime plumbing or removed aliases", () => {
  const values = [
    "ConcurrencyGroup",
    "ExecError",
    "Git",
    "InputError",
    "Job",
    "NoProjectError",
    "Plan",
    "SDKError",
    "Step",
    "StepError",
    "VerifyError",
    "compose",
    "constant",
    "definePipeline",
    "dir",
    "docker",
    "env",
    "exitNotStarted",
    "files",
    "git",
    "gitFromTree",
    "githubOwnerRepo",
    "glob",
    "goModules",
    "ignore",
    "main",
    "memoKey",
    "noMemo",
    "npm",
    "path",
    "readFile",
    "refTo",
    "refToLastRun",
    "repoFiles",
    "services",
    "startup",
    "tree",
    "withCommandEnv",
    "workDir",
    "writeFile",
  ];
  assert.deepEqual(Object.keys(sdk).sort(), values.sort());
  const manifest = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8"));
  assert.equal(manifest.private, undefined);
  assert.equal(manifest.types, "./index.d.ts");
  assert.deepEqual(manifest.exports, { ".": { types: "./index.d.ts", import: "./dist/index.js" } });
});
