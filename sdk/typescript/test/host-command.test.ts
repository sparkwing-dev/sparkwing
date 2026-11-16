import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { ExecError } from "../src/command.ts";
import { commandHost } from "./host-command.ts";

test("host cleanup failure preserves the completed command result", async t => {
  for (const exitCode of [0, 7]) await t.test(String(exitCode), async t => {
    const dir = await mkdtemp(join(tmpdir(), "command-host-failure-"));
    t.after(() => rm(dir, { recursive: true, force: true }));
    const program = join(dir, "host.mjs");
    const frames = [
      { exec_output: "test", stream: "stdout", data: Buffer.from("command-out").toString("base64") },
      { exec_output: "test", stream: "stderr", data: Buffer.from("command-err").toString("base64") },
      { exec_done: "test", exit_code: exitCode },
    ];
    await writeFile(program, `process.stdin.resume(); process.stdin.on("end", () => {
      process.stdout.write(${JSON.stringify(frames.map(frame => JSON.stringify(frame)).join("\n") + "\n")});
      process.stderr.write("cleanup-refused\\n"); process.exitCode = 1;
    });\n`);
    const run = commandHost(process.execPath, [program]);
    await assert.rejects(run({ argv: ["observed-command"], dir, env: [], capture: true }, new AbortController().signal), error => {
      assert.ok(error instanceof ExecError);
      assert.equal(error.exitCode, exitCode);
      assert.equal(error.stdout, "command-out");
      assert.equal(error.stderr, "command-err");
      assert.match(error.message, /cleanup-refused/);
      assert.doesNotMatch(error.message, /failed to start/);
      return true;
    });
  });
});
