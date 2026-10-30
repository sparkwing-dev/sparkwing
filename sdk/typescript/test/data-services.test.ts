import assert from "node:assert/strict";
import { test, type TestContext } from "node:test";
import { definePipeline, refTo, refToLastRun } from "../src/index.ts";
import type { PipelineDef } from "../index.d.ts";
import { resetPipelines } from "../src/plan.ts";
import { NodeRoutes } from "../src/routes.ts";
import { Host, hello } from "./helpers.ts";

async function start(t: TestContext, plan: PipelineDef["plan"], request: typeof fetch) {
  resetPipelines();
  definePipeline({ name: "data", extra: true, plan });
  const host = new Host({ services: (routes) => new NodeRoutes(routes, request) });
  t.after(() => host.close());
  const binding = {
    run_id: "parent",
    job_id: "consumer",
    routes: { url: "http://127.0.0.1:12345", bearer: "hello-only" },
  };
  assert.equal((await host.send({ ...hello, binding })).ok, true);
  const planned = await host.send({
    id: "plan",
    op: "plan",
    pipeline: "data",
    args: { inherited: "parent-only" },
    run: { run_id: "parent", pipeline: "data" },
  });
  assert.equal(planned.ok, true, JSON.stringify(planned));
  return { host, jobs: planned.result.jobs };
}

test("artifact edges, output references and child results reach the bound routes", async (t) => {
  const calls: string[] = [];
  let childWaits = 0;
  const { host, jobs } = await start(
    t,
    (plan) => {
      const producer = plan
        .job("producer", (): { revision: number } => {
          throw new Error("producer must not execute in the consumer");
        })
        .artifacts("dist/**");
      const direct = refTo(producer);
      const previous = refToLastRun<number>("release/candidate", "bundle", { maxAgeMs: 0, fromAnyRun: true });
      plan
        .job("consumer", async (ctx) => ({
          direct: await direct.get(ctx),
          previous: await previous.get(ctx),
          child: await ctx.runPipeline("child", "result", { inputs: { count: 1 }, args: { count: "2" } }),
        }))
        .consumes(producer, { into: "vendor" })
        .references(previous);
    },
    async (url, options) => {
      const target = new URL(String(url));
      calls.push(`${options?.method} ${target.pathname}${target.search}`);
      assert.equal(new Headers(options?.headers).get("authorization"), "Bearer hello-only");
      assert.equal(options?.redirect, "error");
      if (target.pathname === "/node/v1/jobs/producer/output") return Response.json({ output: { revision: 7 } });
      if (target.pathname === "/node/v1/pipelines/release%2Fcandidate/latest") {
        assert.equal(target.searchParams.get("job"), "bundle");
        assert.equal(target.searchParams.get("max_age_ms"), "0");
        assert.equal(target.searchParams.get("from_any_run"), "true");
        return Response.json({ output: 42 });
      }
      if (target.pathname === "/node/v1/children") {
        assert.deepEqual(JSON.parse(String(options?.body)), { pipeline: "child", args: { count: "2" } });
        return Response.json({ run_id: "child-run" }, { status: 201 });
      }
      assert.equal(target.pathname, "/node/v1/children/child-run/await");
      assert.equal(target.searchParams.get("job"), "result");
      assert.equal(target.searchParams.get("wait_ms"), "60000");
      return Response.json(++childWaits === 1 ? { status: "pending" } : { status: "success", output: false });
    },
  );
  assert.deepEqual(calls, []);
  assert.deepEqual(jobs[0].artifacts, ["dist/**"]);
  assert.deepEqual(jobs[1].consumes, [{ producer: "producer", into: "vendor" }]);
  assert.deepEqual(jobs[1].deps, ["producer"]);
  assert.deepEqual(jobs[1].pipeline_refs, [{ pipeline: "release/candidate", job: "bundle", from_any_run: true }]);
  const result = await host.send({ id: "body", op: "run_step", settled: [], job: "consumer", step: jobs[1].steps[0].id, attempt: 1 });
  assert.equal(result.result.outcome, "success", JSON.stringify(result));
  assert.deepEqual(result.result.output, { direct: { revision: 7 }, previous: 42, child: false });
  assert.deepEqual(calls, [
    "GET /node/v1/jobs/producer/output",
    "GET /node/v1/pipelines/release%2Fcandidate/latest?job=bundle&from_any_run=true&max_age_ms=0",
    "POST /node/v1/children",
    "GET /node/v1/children/child-run/await?job=result&wait_ms=60000",
    "GET /node/v1/children/child-run/await?job=result&wait_ms=60000",
  ]);
});

test("optional references suppress only genuine absence through the protocol", async (t) => {
  const cases = [
    { name: "missing", status: 404, body: { error: { code: "not_found", message: "missing output" } }, code: undefined },
    {
      name: "forbidden",
      status: 403,
      body: { error: { code: "forbidden", message: "no reference edge", details: { reason: "not_declared" } } },
      code: "forbidden",
    },
    { name: "not ready", status: 409, body: { error: { code: "conflict", message: "output not ready" } }, code: "conflict" },
    {
      name: "future error",
      status: 503,
      body: { error: { code: "future_error", message: "unavailable", hint: "try again" } },
      code: "future_error",
    },
    { name: "missing payload", status: 200, body: {}, code: "protocol_error" },
  ];
  for (const kind of ["dependency", "previous run"]) {
    for (const entry of cases) {
      await t.test(`${kind}: ${entry.name}`, async (t) => {
        let reads = 0;
        const { host, jobs } = await start(
          t,
          (plan) => {
            const producer = plan.job("producer", () => 1);
            const previous = refToLastRun<number>("release", "producer");
            const ref = kind === "dependency" ? refTo(producer) : previous;
            plan
              .job("consumer", async (ctx) => ({ missing: (await ref.tryGet(ctx)) === undefined }))
              .needs(producer)
              .references(previous);
          },
          async () => {
            reads++;
            return Response.json(entry.body, { status: entry.status });
          },
        );
        const result = await host.send({ id: "body", op: "run_step", settled: [], job: "consumer", step: jobs[1].steps[0].id, attempt: 1 });
        assert.equal(reads, 1);
        assert.equal(result.ok, true);
        if (entry.code === undefined) {
          assert.deepEqual(result.result, { outcome: "success", output: { missing: true } });
        } else {
          assert.equal(result.result.outcome, "failed");
          assert.equal(result.result.error.code, entry.code);
          if ("error" in entry.body) assert.deepEqual(result.result.error, entry.body.error);
        }
      });
    }
  }
});

test("a child's canonical failure survives the parent's body reply", async (t) => {
  const error = { code: "forbidden", message: "child repository refused", details: { reason: "child_repo" } };
  const { host, jobs } = await start(
    t,
    (plan) => {
      plan.job("consumer", (ctx) => ctx.runPipeline("child", "result"));
    },
    async (url) =>
      String(url).endsWith("/children")
        ? Response.json({ run_id: "child-run" }, { status: 201 })
        : Response.json({ status: "failed", error }),
  );
  const result = await host.send({ id: "body", op: "run_step", settled: [], job: "consumer", step: jobs[0].steps[0].id, attempt: 1 });
  assert.deepEqual(result, { reply: "body", ok: true, result: { outcome: "failed", error } });
});
