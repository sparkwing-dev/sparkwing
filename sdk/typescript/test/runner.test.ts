import assert from "node:assert/strict";
import { beforeEach, test } from "node:test";
import { definePipeline, resetPipelines } from "../src/plan.ts";
import { NodeRoutes } from "../src/routes.ts";
import { converse } from "./helpers.ts";

const plan = { id: "p", op: "plan", pipeline: "ship", args: { env: "prod" }, run: { run_id: "run-1", pipeline: "ship", git_sha: "abc" } };
let planCalls = 0;
let release: () => void = () => undefined;

beforeEach(() => {
  resetPipelines();
  planCalls = 0;
  definePipeline({
    name: "ship",
    plan(p, run) {
      planCalls++;
      const build = p.job("build", (node) => {
        node.log.info(`building for ${run.args["env"]} at ${String(run.git["git_sha"])}, attempt ${node.attempt}, dry ${node.dryRun}`);
        return { digest: "sha-1" };
      });
      p.job("broken", () => {
        throw new Error("compiler exploded");
      });
      p.job("slow", () => new Promise<void>((resolve) => (release = resolve)));
      const deploy = p.job("deploy").needs(build).skipIf(() => {
        release();
        return run.args["env"] !== "prod";
      });
      deploy.step("apply", async (node) => {
        const out = await node.output<{ digest: string }>("build");
        node.log.info(`applying ${out.digest}`);
      });
    },
  });
});

test("describe and plan reply by request id", async () => {
  const out = await converse([{ id: "d", op: "describe" }, plan]);
  assert.equal(out.reply("d")?.["ok"], true);
  assert.equal((out.reply("d")?.["result"] as { protocol: string }).protocol, "1");
  const doc = out.reply("p")?.["result"] as { run_id: string; nodes: Array<{ id: string }> };
  assert.equal(doc.run_id, "run-1");
  assert.deepEqual(doc.nodes.map((n) => n.id), ["build", "broken", "slow", "deploy"]);
});

test("run_node returns the body's output, and its log records are node-scoped", async () => {
  const out = await converse([plan, { id: "n", op: "run_node", node: "build", attempt: 2, options: { dry_run: true } }]);
  assert.deepEqual(out.reply("n"), { reply: "n", ok: true, result: { outcome: "success", output: { digest: "sha-1" } } });
  const rec = out.records()[0];
  assert.equal(rec?.["node"], "build");
  assert.equal(rec?.["msg"], "building for prod at abc, attempt 2, dry true");
  assert.equal(planCalls, 1);
});

test("a throwing body is a failed outcome, not a failed reply", async () => {
  const out = await converse([plan, { id: "n", op: "run_node", node: "broken" }]);
  assert.deepEqual(out.reply("n"), { reply: "n", ok: true, result: { outcome: "failed", error: "compiler exploded" } });
  assert.equal(out.records()[0]?.["level"], "error");
});

test("run_step runs one step and reads a dependency's output through the routes", async () => {
  const fake = (async (input: string | URL | Request) => {
    const url = String(input);
    if (url === "http://engine/node/v1/nodes/build/output") return new Response(JSON.stringify({ url: "/blob" }));
    if (url === "http://engine/blob") return new Response(JSON.stringify({ digest: "sha-1" }));
    return new Response("", { status: 404 });
  }) as typeof fetch;
  const routes = new NodeRoutes({ baseUrl: "http://engine", token: "t", fetch: fake });
  const out = await converse([plan, { id: "s", op: "run_step", node: "deploy", step: "apply" }], routes);
  assert.deepEqual(out.reply("s"), { reply: "s", ok: true, result: { outcome: "success" } });
  assert.deepEqual(
    out.records().map((r) => [r["node"], r["step"], r["msg"]]),
    [["deploy", "apply", "applying sha-1"]],
  );
});

test("eval evaluates a closure by the id the plan listed", async () => {
  const dev = { ...plan, id: "p2", args: { env: "dev" } };
  const out = await converse([
    plan,
    { id: "e1", op: "eval", closure: "deploy/skip_if/0" },
    dev,
    { id: "e2", op: "eval", closure: "deploy/skip_if/0" },
    { id: "e3", op: "eval", closure: "build/skip_if/0" },
  ]);
  assert.deepEqual(out.reply("e1")?.["result"], { value: false });
  assert.deepEqual(out.reply("e2")?.["result"], { value: true });
  assert.deepEqual(out.reply("e3")?.["error"], { message: "no closure build/skip_if/0 in this plan" });
});

test("an eval is answered while a node body is still running; the body waits on it", async () => {
  const out = await converse([plan, { id: "n", op: "run_node", node: "slow" }, { id: "e", op: "eval", closure: "deploy/skip_if/0" }]);
  const order = out.replies().map((r) => r["reply"]);
  assert.deepEqual(order, ["p", "e", "n"]);
});

test("bad requests get a failed reply, and a line with no id becomes a log record", async () => {
  const out = await converse([
    "not json",
    { op: "describe" },
    { id: "a", op: "run_node", node: "build" },
    plan,
    { id: "b", op: "teleport" },
    { id: "c", op: "run_node", node: "ghost" },
    { id: "d", op: "run_node", node: "deploy" },
    { id: "e", op: "plan", pipeline: "nope", run: { run_id: "r" } },
    { id: "f", op: "plan", pipeline: "ship", args: { n: 1 }, run: { run_id: "r" } },
    { id: "g", op: "plan", pipeline: "ship" },
  ]);
  const errors = Object.fromEntries(out.replies().filter((r) => r["ok"] === false).map((r) => [r["reply"], (r["error"] as { message: string }).message]));
  assert.deepEqual(errors, {
    a: "no plan in this process yet; the engine sends plan first",
    b: "unknown op teleport",
    c: "pipeline ship has no job ghost",
    d: "job deploy has steps; the engine runs them with run_step",
    e: "no pipeline nope",
    f: "args.n must be a string",
    g: "run must be an object",
  });
  const logged = out.records().map((r) => String(r["msg"]));
  assert.equal(logged.length, 2);
  assert.match(logged[0] ?? "", /request is not JSON/);
  assert.match(logged[1] ?? "", /needs a non-empty string id/);
});
