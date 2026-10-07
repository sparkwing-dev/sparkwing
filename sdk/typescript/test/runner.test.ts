import assert from "node:assert/strict";
import { beforeEach, test } from "node:test";
import { definePipeline, resetPipelines } from "../src/plan.ts";
import { NodeRoutes } from "../src/routes.ts";
import { converse } from "./helpers.ts";

const run = { pipeline: "ship", run_id: "run-1", args: { env: "prod" } };
let planCalls = 0;

beforeEach(() => {
  resetPipelines();
  planCalls = 0;
  definePipeline({
    name: "ship",
    plan(plan, ctx) {
      planCalls++;
      const build = plan.job("build", (node) => {
        node.log.info(`building for ${ctx.args["env"]}`);
        return { digest: "sha-1" };
      });
      plan.job("broken", () => {
        throw new Error("compiler exploded");
      });
      const deploy = plan.job("deploy").needs(build).skipIf((node) => node.args["env"] !== "prod");
      deploy.step("apply", async (node) => {
        const out = await node.output<{ digest: string }>("build");
        node.log.info(`applying ${out.digest}`);
      });
    },
  });
});

test("describe and plan answer by request id", async () => {
  const out = await converse([
    { id: 1, method: "describe" },
    { id: 2, method: "plan", params: run },
  ]);
  const [describe, plan] = out.responses();
  assert.equal((describe?.["result"] as { protocol: number }).protocol, 1);
  const nodes = (plan?.["result"] as { nodes: Array<{ id: string }> }).nodes.map((n) => n.id);
  assert.deepEqual(nodes, ["build", "broken", "deploy"]);
});

test("run_node returns the body's output and its log records are node-scoped", async () => {
  const out = await converse([{ id: 7, method: "run_node", params: { ...run, node: "build" } }]);
  assert.deepEqual(out.responses(), [{ id: 7, result: { outcome: "success", output: { digest: "sha-1" } } }]);
  const rec = out.records()[0];
  assert.equal(rec?.["node"], "build");
  assert.equal(rec?.["msg"], "building for prod");
});

test("a throwing body is a failed outcome, not a protocol error", async () => {
  const out = await converse([{ id: 1, method: "run_node", params: { ...run, node: "broken" } }]);
  assert.deepEqual(out.responses(), [{ id: 1, result: { outcome: "failed", error: "compiler exploded" } }]);
  assert.equal(out.records()[0]?.["level"], "error");
});

test("run_step runs one step and reads a dependency's output through the routes", async () => {
  const fake = (async (url: string | URL | Request) => {
    assert.equal(String(url), "http://engine/node/v1/outputs/build");
    return new Response(JSON.stringify({ digest: "sha-1" }), { status: 200 });
  }) as typeof fetch;
  const routes = new NodeRoutes({ baseUrl: "http://engine", token: "t", fetch: fake });
  const out = await converse([{ id: 3, method: "run_step", params: { ...run, node: "deploy", step: "apply" } }], routes);
  assert.deepEqual(out.responses(), [{ id: 3, result: { outcome: "success" } }]);
  assert.deepEqual(
    out.records().map((r) => [r["node"], r["step"], r["msg"]]),
    [["deploy", "apply", "applying sha-1"]],
  );
});

test("call evaluates a closure by id", async () => {
  const out = await converse([
    { id: 1, method: "call", params: { ...run, closure: "deploy/skip_if" } },
    { id: 2, method: "call", params: { ...run, args: { env: "dev" }, closure: "deploy/skip_if" } },
    { id: 3, method: "call", params: { ...run, closure: "build/skip_if" } },
  ]);
  const [prod, dev, missing] = out.responses();
  assert.deepEqual(prod?.["result"], { value: false });
  assert.deepEqual(dev?.["result"], { value: true });
  assert.equal((missing?.["error"] as { code: string }).code, "unknown_closure");
});

test("one process plans once per run and args", async () => {
  await converse([
    { id: 1, method: "plan", params: run },
    { id: 2, method: "run_node", params: { ...run, node: "build" } },
  ]);
  assert.equal(planCalls, 1);
});

test("bad requests are answered with a code, and shutdown stops the loop", async () => {
  const out = await converse([
    "not json",
    { id: 1, method: "teleport" },
    { id: 2, method: "run_node", params: { ...run, node: "ghost" } },
    { id: 3, method: "run_node", params: { ...run, node: "deploy" } },
    { id: 4, method: "plan", params: { ...run, pipeline: "nope" } },
    { id: 5, method: "plan", params: { ...run, args: { n: 1 } } },
    { id: 6, method: "shutdown" },
    { id: 7, method: "describe" },
  ]);
  const codes = out.responses().map((r) => [r["id"], (r["error"] as { code?: string } | undefined)?.code ?? "ok"]);
  assert.deepEqual(codes, [
    [1, "unknown_method"],
    [2, "unknown_node"],
    [3, "bad_request"],
    [4, "unknown_pipeline"],
    [5, "bad_request"],
    [6, "ok"],
  ]);
  assert.match(String(out.records()[0]?.["msg"]), /request is not JSON/);
});
