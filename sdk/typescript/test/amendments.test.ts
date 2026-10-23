import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { decodeError } from "../src/errors.ts";
import { parseJSON } from "../src/json.ts";
import { ConcurrencyGroup, Plan, definePipeline, resetPipelines } from "../src/plan.ts";
import { Host, hello } from "./helpers.ts";
import { NodeRoutes } from "../src/routes.ts";

const corpus = JSON.parse(
  readFileSync(new URL("../../../pkg/nodeproto/conformance/testdata/v2-corpus.json", import.meta.url), "utf8"),
);

test("shared concurrency and source vectors preserve omission and explicit defaults", () => {
  const declarations: Record<string, Plan> = {
    "concurrency-omitted": new Plan().concurrency(new ConcurrencyGroup("gate")),
    "concurrency-explicit-defaults": new Plan().concurrency(
      new ConcurrencyGroup("gate", { scope: "global", capacity: 1, onLimit: "queue" }),
      1,
    ),
    "source-omitted": new Plan().checkout({}),
    "source-explicit-false": new Plan().checkout({ tags: false, fullHistory: false }),
  };
  for (const vector of corpus.plans) {
    const plan = declarations[vector.name];
    if (!plan) continue;
    const doc = plan.toDoc();
    assert.deepEqual(doc, JSON.parse(vector.raw), vector.name);
    const canonical = JSON.stringify(
      doc,
      Object.keys(JSON.parse(vector.canonical)).concat(["group", "scope", "capacity", "cost", "on_limit", "tags", "full_history"]).sort(),
    );
    assert.equal(createHash("sha256").update(canonical).digest("hex"), vector.sha256, vector.name);
  }
  const p = new Plan();
  const job = p.job("job", () => null);
  job.concurrency(new ConcurrencyGroup("gate"), 3);
  assert.deepEqual(p.toDoc().jobs[0]!.concurrency, [{ group: "gate", cost: 3 }]);
  assert.throws(() => new ConcurrencyGroup("gate", { capacity: 0 }), /positive/);
  assert.throws(() => job.concurrency(new ConcurrencyGroup("gate"), 0), /positive/);
});

test("shared inner error vectors reject duplicate, null and nested error envelopes", () => {
  for (const vector of corpus.inner_errors) {
    if (vector.valid) assert.ok(decodeError(parseJSON(vector.raw)), vector.name);
    else assert.throws(() => decodeError(parseJSON(vector.raw)), vector.name);
  }
  for (const key of ["message", "code", "hint", "details", "reason", "__proto__", "constructor"]) {
    for (const encoded of [
      JSON.stringify(key),
      '"' + [...key].map((c) => "\\u" + c.charCodeAt(0).toString(16).padStart(4, "0")).join("") + '"',
    ]) {
      assert.throws(() => parseJSON(`{"nested":[{${JSON.stringify(key)}:1,${encoded}:2}]}`), /duplicate JSON field/);
      assert.deepEqual(parseJSON(`{"a":{${encoded}:1},"b":{${encoded}:2}}`), JSON.parse(`{"a":{${encoded}:1},"b":{${encoded}:2}}`));
    }
  }
});

test("record routes preserve step attribution and defer masked record bounds to the host", async () => {
  const requests: { path: string; body: any }[] = [];
  const services = new NodeRoutes({ url: "http://127.0.0.1:12345", bearer: "private" }, async (path, options) => {
    requests.push({ path: String(path), body: JSON.parse(String(options?.body)) });
    return new Response("{}");
  });
  resetPipelines();
  const text = "🌱".repeat(40000);
  definePipeline({
    name: "records",
    plan(p) {
      p.job("job", async (ctx) => {
        await ctx.annotate({ message: text, line: 1, endLine: 2 });
        await ctx.summary(text);
      });
    },
  });
  const host = new Host({ services: () => services });
  try {
    await host.send({ ...hello, binding: { run_id: "r", job_id: "job", routes: { url: "http://127.0.0.1:12345", bearer: "private" } } });
    await host.send({ id: "plan", op: "plan", pipeline: "records", args: {}, run: { run_id: "r", pipeline: "records" } });
    const result = await host.send({ id: "body", op: "run_step", job: "job", step: "run", attempt: 1, settled: [] });
    assert.equal(result.result.outcome, "success", JSON.stringify(result));
    assert.deepEqual(
      requests.map((r) => r.body),
      [
        { message: text, level: "info", line: 1, end_line: 2, step: "run" },
        { markdown: text, step: "run" },
      ],
    );
  } finally {
    await host.close();
  }
});

test("foreign cancellation selectors cannot abort work bound to another job", async () => {
  resetPipelines();
  let enter!: () => void, release!: () => void;
  const entered = new Promise<void>((resolve) => {
    enter = resolve;
  });
  const barrier = new Promise<void>((resolve) => {
    release = resolve;
  });
  let aborted = false;
  definePipeline({
    name: "bound",
    plan(p) {
      p.job("job", async (ctx) => {
        enter();
        await barrier;
        aborted = ctx.signal.aborted;
      });
    },
  });
  const host = new Host();
  try {
    await host.send({ ...hello, binding: { run_id: "r", job_id: "job", routes: { url: "http://127.0.0.1:1", bearer: "private" } } });
    await host.send({ id: "plan", op: "plan", pipeline: "bound", args: {}, run: { run_id: "r", pipeline: "bound" } });
    const pending = host.send({ id: "body", op: "run_step", job: "job", step: "run", attempt: 1, settled: [] });
    await entered;
    let seed = 104;
    const foreign = ["foreign", "__proto__", "constructor", "job/child", "Job", " job", "job\u0000"];
    for (let i = 0; i < 128; i++) {
      let job = "";
      for (let j = 0; j < 1 + (i % 20); j++) {
        seed = (seed * 1664525 + 1013904223) >>> 0;
        job += String.fromCharCode(seed % 128);
      }
      if (job !== "job") foreign.push(job);
    }
    for (const [index, job] of foreign.entries()) {
      const response = await host.send({ id: `foreign-${index}`, op: "cancel", job });
      assert.equal(response.error?.code, "protocol_error");
    }
    release();
    assert.equal((await pending).result.outcome, "success");
    assert.equal(aborted, false);
  } finally {
    release();
    await host.close();
  }
});

test("refused mask registrations propagate safe field diagnostics without echoing submitted values", async () => {
  const signal = new AbortController().signal;
  let seed = 104;
  for (let n = 0; n < 32; n++) {
    seed = (seed * 1664525 + 1013904223) >>> 0;
    const value = `mask-${seed.toString(16)}-${n}`;
    const routes = new NodeRoutes({ url: "http://127.0.0.1:12345", bearer: "private" }, async (_path, options) => {
      assert.deepEqual(JSON.parse(String(options?.body)), { value });
      return new Response(
        JSON.stringify({
          error: { code: "secret_in_identifier", message: "mask matches an accepted identifier", details: { field: "jobs[0].id" } },
        }),
        { status: 400 },
      );
    });
    await assert.rejects(routes.mask(value, signal), (error: any) => {
      assert.equal(error.code, "secret_in_identifier");
      assert.equal(error.details.field, "jobs[0].id");
      assert.equal((JSON.stringify(error) + error.message).includes(value), false);
      return true;
    });
  }
});
