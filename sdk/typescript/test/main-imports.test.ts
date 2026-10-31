import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { Readable, Writable } from "node:stream";
import { test } from "node:test";
import { hello } from "./helpers.ts";

test("main preserves default and named imports from symlinked CommonJS and ESM packages", { skip: process.platform === "win32" }, async (t) => {
  const root = mkdtempSync(join(tmpdir(), "sdk-main-imports-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const modules = join(root, "node_modules");
  mkdirSync(modules);
  for (const [name, type, source] of [
    ["cjs-fixture", "commonjs", "exports.ScriptTarget = { Latest: 99 };"],
    ["esm-fixture", "module", "export const ScriptTarget = { Latest: 99 }; export default { ScriptTarget };"],
  ] as const) {
    const target = join(root, "packages", name);
    mkdirSync(target, { recursive: true });
    writeFileSync(join(target, "package.json"), JSON.stringify({ name, type, main: "index.js" }));
    writeFileSync(join(target, "index.js"), source);
    symlinkSync(target, join(modules, name), "dir");
  }
  const probe = `
    const cjs = await import("cjs-fixture");
    const esm = await import("esm-fixture");
    return [cjs.default.ScriptTarget.Latest, cjs.ScriptTarget.Latest,
      cjs.default.ScriptTarget === cjs.ScriptTarget,
      esm.default.ScriptTarget.Latest, esm.ScriptTarget.Latest];
  `;
  const plain = join(root, "plain.mjs");
  writeFileSync(plain, `console.log(JSON.stringify(await (async () => { ${probe} })()));`);
  const baseline = spawnSync(process.execPath, [plain], { encoding: "utf8" });
  assert.equal(baseline.status, 0, baseline.stderr);
  const expected = JSON.parse(baseline.stdout);
  assert.deepEqual(expected, [99, 99, true, 99, 99]);

  const entry = join(root, "pipeline.mjs");
  writeFileSync(entry, `
    import { definePipeline, main } from ${JSON.stringify(new URL("../src/index.ts", import.meta.url).href)};
    definePipeline({ name: "imports", plan(plan) {
      plan.job("check", async () => { ${probe} });
    } });
    await main();
  `);
  const child = spawn(process.execPath, [entry, "--sw-node-protocol"], {
    stdio: ["ignore", "pipe", "pipe", "pipe", "pipe"],
    signal: AbortSignal.timeout(10000),
  });
  const closed = once(child, "close");
  t.after(() => child.kill());
  let stderr = "";
  child.stderr!.on("data", (chunk) => { stderr += chunk; });
  child.stdout!.resume();
  const requests = child.stdio[3];
  const frames = child.stdio[4];
  assert.ok(requests instanceof Writable);
  assert.ok(frames instanceof Readable);
  const lines = createInterface({ input: frames });
  const replies = lines[Symbol.asyncIterator]();
  const send = async (request: Record<string, unknown>) => {
    requests.write(JSON.stringify(request) + "\n");
    const line = await replies.next();
    assert.equal(line.done, false, stderr);
    const reply = JSON.parse(line.value!);
    assert.equal(reply.ok, true, JSON.stringify(reply));
    return reply.result;
  };
  try {
    await send({ ...hello, binding: { run_id: "r1", job_id: "check", routes: { url: "http://127.0.0.1:12345", bearer: "test" } } });
    await send({ id: "plan", op: "plan", pipeline: "imports", args: {}, run: { run_id: "r1", pipeline: "imports" } });
    const result = await send({ id: "step", op: "run_step", job: "check", step: "run", attempt: 1, settled: [] });
    assert.equal(result.outcome, "success", JSON.stringify(result));
    assert.deepEqual(result.output, expected);
  } finally {
    requests.end();
    const [code] = await closed;
    assert.equal(code, 0, stderr);
    lines.close();
  }
});
