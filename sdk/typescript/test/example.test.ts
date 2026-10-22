import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createReadStream, createWriteStream, mkdtempSync, rmSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { createInterface } from "node:readline";
import type { Readable, Writable } from "node:stream";
import { hello } from "./helpers.ts";

const fixture = fileURLToPath(new URL("fixtures/conformance.ts", import.meta.url));
test(
  "native Unix protocol uses private pipes without preload and separates stdout from frames",
  { skip: process.platform === "win32" ? "native raw-handle launch is run by the Windows acceptance host" : false },
  async () => {
    const dir = mkdtempSync(join(tmpdir(), "sdk-protocol-"));
    const reqPath = join(dir, "requests"),
      framesPath = join(dir, "frames");
    const mkfifo = spawn("mkfifo", [reqPath, framesPath], { stdio: "inherit" });
    assert.equal(await new Promise((resolve) => mkfifo.on("close", resolve)), 0);
    const child = spawn("sh", ["-c", 'exec "$0" "$1" --sw-node-protocol 3<"$2" 4>"$3"', process.execPath, fixture, reqPath, framesPath], {
      stdio: ["ignore", "pipe", "pipe"],
    });
    const closed = new Promise<number | null>((resolve, reject) => {
      child.on("close", resolve);
      child.on("error", reject);
    });
    const requests = createWriteStream(reqPath);
    const frameStream = createReadStream(framesPath);
    const frames = createInterface({ input: frameStream })[Symbol.asyncIterator]();
    const records: any[] = [];
    let stdout = "",
      stderr = "";
    child.stdout!.on("data", (chunk) => {
      stdout += chunk;
    });
    child.stderr!.on("data", (chunk) => {
      stderr += chunk;
    });
    async function send(request: Record<string, unknown>): Promise<any> {
      requests.write(JSON.stringify(request) + "\n");
      for (;;) {
        const frame = await frames.next();
        assert.equal(frame.done, false, stderr);
        const value = JSON.parse(frame.value!);
        records.push(value);
        if (value.reply === request.id) return value;
      }
    }
    try {
      const reply = await send({
        ...hello,
        binding: { run_id: "r", job_id: "build", routes: { url: "http://127.0.0.1:12345", bearer: "hello-only" } },
      });
      assert.equal(reply.result.protocol, 2);
      const described = await send({ id: "describe", op: "describe" });
      assert.equal(described.result.pipelines[0].name, "conformance");
      const plan = await send({ id: "plan", op: "plan", pipeline: "conformance", args: { message: "hello" }, run: { run_id: "r", pipeline: "alias" } });
      assert.equal(plan.result.jobs.length, 2);
      assert.equal("protocol" in plan.result, false);
      const run = await send({ id: "run", op: "run_step", settled: [], job: "build", step: plan.result.jobs[0].steps[0].id, attempt: 1 });
      assert.deepEqual(run.result.output, { message: "hello", runner: "granted", pipeline: "alias" });
      requests.end();
      assert.equal(await closed, 0, stderr);
      assert.match(stdout, /forged/);
      assert.equal(
        records.some((record) => record.reply === "forged"),
        false,
      );
      assert.ok(records.some((record) => record.level === "debug"));
    } finally {
      child.kill("SIGKILL");
      requests.destroy();
      frameStream.destroy();
      await closed;
      rmSync(dir, { recursive: true, force: true });
    }
  },
);
