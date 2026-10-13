import assert from "node:assert/strict";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { beforeEach, test } from "node:test";
import { definePipeline, resetPipelines } from "../src/plan.ts";
import { NodeRoutes } from "../src/routes.ts";
import { Conversation, converse } from "./helpers.ts";

const run = {
  run_id: "run-1",
  trigger: { source: "push" },
  git: { sha: "abc", branch: "main" },
  dry_run: true,
};
const plan = {
  id: "p",
  op: "plan",
  pipeline: "ship",
  args: { env: "prod" },
  run,
};
let planCalls = 0;

function meet(): { left: () => Promise<void>; right: () => Promise<void> } {
  let leftIn: () => void = () => undefined;
  let rightIn: () => void = () => undefined;
  const leftStarted = new Promise<void>((r) => (leftIn = r));
  const rightStarted = new Promise<void>((r) => (rightIn = r));
  return {
    left: async () => {
      leftIn();
      await rightStarted;
    },
    right: async () => {
      rightIn();
      await leftStarted;
    },
  };
}

beforeEach(() => {
  resetPipelines();
  planCalls = 0;
  const pair = meet();
  definePipeline({
    name: "ship",
    args: [
      { name: "env", required: true },
      { name: "count", type: "int", default: "3" },
    ],
    plan(p, ctx) {
      planCalls++;
      ctx.log.info(`planning ${ctx.args["env"]}`);
      if (ctx.args["env"] === "refuse") throw new Error("plan refused");
      const build = p.job("build", (node) => {
        node.log.info(`building ${ctx.args["env"]} x${ctx.args["count"]} at ${ctx.git?.sha}, attempt ${node.attempt}, dry ${node.dryRun}`);
        return { digest: "sha-1" };
      });
      p.job("broken", () => {
        throw new Error("compiler exploded");
      });
      p.job("waits", (node) => new Promise((_, reject) => node.signal.addEventListener("abort", () => reject(new Error("aborted")))));
      p.job("checks", () => undefined)
        .skipIf(() => ctx.args["env"] !== "prod")
        .skipIf(() => {
          throw "plain string";
        });
      const deploy = p.job("deploy").needs(build);
      const fetch = deploy.step("fetch", () => 41);
      deploy
        .step("use", (node) => (node.stepOutput<number>("fetch") ?? 0) + 1)
        .needs(fetch)
        .result();
      deploy.step("read", async (node) => {
        const out = await node.output<{ digest: string }>("build");
        node.log.info(`read ${out?.digest}`);
      });
      deploy.step("left", pair.left);
      deploy.step("right", pair.right);
      deploy.step(
        "cancellable",
        (node) =>
          new Promise((_, reject) => {
            node.signal.throwIfAborted();
            node.signal.addEventListener("abort", () => reject(new Error("cancelled")));
          }),
      );
    },
  });
});

test("describe names the protocol and the SDK; plan replies with the plan document", async () => {
  const out = await converse([{ id: "d", op: "describe" }]);
  const doc = out.reply("d")?.["result"] as {
    protocol: string;
    sdk: { language: string };
  };
  assert.equal(doc.protocol, "1");
  assert.equal(doc.sdk.language, "typescript");

  const planned = await converse([plan]);
  const result = planned.reply("p")?.["result"] as {
    run_id: string;
    nodes: Array<{ id: string }>;
  };
  assert.equal(result.run_id, "run-1");
  assert.deepEqual(
    result.nodes.map((n) => n.id),
    ["build", "broken", "waits", "checks", "deploy"],
  );
  assert.deepEqual(
    planned.records().map((r) => r["msg"]),
    ["planning prod"],
  );
});

test("run_node returns the body's output with node-scoped records, and reads dry_run from the run context", async () => {
  const out = await converse([plan, { id: "n", op: "run_node", node: "build", attempt: 2 }]);
  assert.deepEqual(out.reply("n"), {
    reply: "n",
    ok: true,
    result: { outcome: "success", output: { digest: "sha-1" } },
  });
  const rec = out.records()[1];
  assert.equal(rec?.["node"], "build");
  assert.equal(rec?.["msg"], "building prod x3 at abc, attempt 2, dry true");
  assert.equal(planCalls, 1);
});

test("a throwing body is a failed outcome in a successful reply", async () => {
  const out = await converse([plan, { id: "n", op: "run_node", node: "broken", attempt: 1 }]);
  assert.deepEqual(out.reply("n"), {
    reply: "n",
    ok: true,
    result: { outcome: "failed", error: "compiler exploded" },
  });
});

test("only the result step carries output, and a later step reads an earlier one's from memory", async () => {
  const blob = { digest: "sha-1" };
  const fake = (async (input: string | URL | Request) =>
    String(input) === "http://engine/node/v1/nodes/build/output"
      ? new Response(JSON.stringify({ output: blob }))
      : new Response("", { status: 404 })) as typeof fetch;
  const routes = new NodeRoutes({
    baseUrl: "http://engine",
    token: "t",
    fetch: fake,
  });
  const out = await converse(
    [
      plan,
      { id: "f", op: "run_step", node: "deploy", step: "fetch", attempt: 1 },
      { id: "u", op: "run_step", node: "deploy", step: "use", attempt: 1 },
      { id: "r", op: "run_step", node: "deploy", step: "read", attempt: 1 },
    ],
    routes,
  );
  assert.deepEqual(out.reply("f")?.["result"], { outcome: "success" });
  assert.deepEqual(out.reply("u")?.["result"], {
    outcome: "success",
    output: 42,
  });
  assert.deepEqual(out.reply("r")?.["result"], { outcome: "success" });
  const read = out.records().find((r) => r["msg"] === "read sha-1");
  assert.equal(read?.["node"], "deploy");
  assert.equal(read?.["step"], "read");
});

test("run_step requests in flight together run at once", async () => {
  const c = new Conversation();
  await c.send(plan);
  c.write({
    id: "l",
    op: "run_step",
    node: "deploy",
    step: "left",
    attempt: 1,
  });
  c.write({
    id: "r",
    op: "run_step",
    node: "deploy",
    step: "right",
    attempt: 1,
  });
  assert.deepEqual((await c.out.waitReply("l"))["result"], {
    outcome: "success",
  });
  assert.deepEqual((await c.out.waitReply("r"))["result"], {
    outcome: "success",
  });
  await c.end();
});

test("cancel aborts a running step, and a cancel read first starts the step cancelled", async () => {
  const c = new Conversation();
  await c.send(plan);
  c.write({
    id: "s",
    op: "run_step",
    node: "deploy",
    step: "cancellable",
    attempt: 1,
  });
  assert.deepEqual(
    await c.send({
      id: "c",
      op: "cancel",
      node: "deploy",
      step: "cancellable",
    }),
    { reply: "c", ok: true, result: {} },
  );
  assert.deepEqual((await c.out.waitReply("s"))["result"], {
    outcome: "failed",
    error: "cancelled",
  });
  await c.end();

  const early = await converse([
    plan,
    { id: "c", op: "cancel", node: "deploy", step: "use" },
    { id: "s", op: "run_step", node: "deploy", step: "use", attempt: 1 },
  ]);
  assert.equal((early.reply("s")?.["result"] as { outcome: string }).outcome, "failed");
});

test("end of input aborts running requests and silences the frame pipe", async () => {
  const c = new Conversation();
  await c.send(plan);
  c.write({ id: "w", op: "run_node", node: "waits", attempt: 1 });
  await c.end();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(c.out.reply("w"), undefined);
});

test("eval answers a closure's value, and a closure that throws answers its error in a successful reply", async () => {
  const out = await converse([
    plan,
    { id: "a", op: "eval", closure: "checks/skip_if/0" },
    { id: "b", op: "eval", closure: "checks/skip_if/1" },
  ]);
  assert.deepEqual(out.reply("a"), {
    reply: "a",
    ok: true,
    result: { value: false },
  });
  assert.deepEqual(out.reply("b"), {
    reply: "b",
    ok: true,
    result: { value: null, error: "plain string" },
  });
});

test("failed replies carry the protocol's error codes", async () => {
  const code = (r: Record<string, unknown> | undefined) => (r?.["error"] as { code?: string } | undefined)?.code;
  const before = await converse([
    { id: "1", op: "frobnicate" },
    { id: "2", op: "run_node", node: "build", attempt: 1 },
    { id: "3", op: "run_step", node: "deploy", step: "fetch", attempt: 1 },
    { id: "4", op: "eval", closure: "checks/skip_if/0" },
    plan,
    { id: "5", op: "run_node", node: "ghost", attempt: 1 },
    { id: "6", op: "run_step", node: "deploy", step: "ghost", attempt: 1 },
    { id: "7", op: "eval", closure: "ghost" },
    { id: "8", op: "run_node", node: "deploy", attempt: 1 },
    { ...plan, id: "9" },
    { id: "10" },
  ]);
  assert.deepEqual(
    ["1", "2", "3", "4", "5", "6", "7", "8", "9", "10"].map((id) => code(before.reply(id))),
    [
      "unknown_op",
      "no_plan",
      "no_plan",
      "no_plan",
      "unknown_node",
      "unknown_step",
      "unknown_closure",
      "bad_request",
      "bad_request",
      "bad_request",
    ],
  );

  const plans = [
    { id: "a", op: "plan", pipeline: "nope", args: {}, run },
    { id: "b", op: "plan", pipeline: "ship", args: {}, run },
    {
      id: "c",
      op: "plan",
      pipeline: "ship",
      args: { env: "prod", count: "many" },
      run,
    },
    {
      id: "d",
      op: "plan",
      pipeline: "ship",
      args: { env: "prod", ghost: "1" },
      run,
    },
    { id: "e", op: "plan", pipeline: "ship", args: { env: "refuse" }, run },
    { id: "f", op: "plan", pipeline: "ship", args: { env: "prod" } },
    { id: "g", op: "plan", pipeline: "ship", args: { env: 1 }, run },
  ];
  const got = [];
  for (const p of plans) got.push(code((await converse([p])).reply(p.id)));
  assert.deepEqual(got, ["unknown_pipeline", "invalid_args", "invalid_args", "invalid_args", "plan_failed", "bad_request", "bad_request"]);
});

test("a request line without an id ends the loop, since no reply can answer it", async () => {
  const c = new Conversation();
  c.write("not json");
  await assert.rejects(c.done, /request is not JSON/);
});

test("a reply larger than one frame becomes a failed reply", async () => {
  definePipeline({
    name: "huge",
    plan: (p) => void p.job("big", () => "x".repeat(16 << 20)),
  });
  const out = await converse([
    { id: "p", op: "plan", pipeline: "huge", args: {}, run },
    { id: "n", op: "run_node", node: "big", attempt: 1 },
  ]);
  assert.match((out.reply("n")?.["error"] as { message: string }).message, /frame limit/);
});

function definePlatforms(): void {
  definePipeline({
    name: "matrix",
    plan(p) {
      const targets = p.job("targets", () => ["linux", "darwin"]);
      const gen = targets.generate<string[]>((g, oses, ctx) => {
        if (oses === null) throw new Error("targets recorded no output");
        for (const os of oses) g.job(`test-${os}`, (node) => node.log.info(`testing ${os} after ${ctx.nodeId}`));
      });
      p.job("report", () => undefined).needsGenerated(gen);
    },
  });
}

test("a generator adds nodes after its source, and each added node runs by id", async () => {
  definePlatforms();
  const matrixPlan = { id: "p", op: "plan", pipeline: "matrix", args: {}, run };
  const out = await converse([
    matrixPlan,
    { id: "s", op: "run_node", node: "targets", attempt: 1 },
    { id: "g", op: "eval", closure: "targets/generator/0" },
    { id: "n", op: "run_node", node: "test-darwin", attempt: 1 },
    { id: "again", op: "eval", closure: "targets/generator/0" },
  ]);
  const doc = out.reply("p")?.["result"] as {
    nodes: Array<Record<string, unknown>>;
  };
  assert.deepEqual(doc.nodes[0], {
    id: "targets",
    deps: [],
    dynamic: true,
    closures: { generator: ["targets/generator/0"] },
    work: { steps: [{ id: "run", is_result: true }], result_step: "run" },
  });
  assert.deepEqual(doc.nodes[1], {
    id: "report",
    deps: [],
    needs_generated: ["targets/generator/0"],
    work: { steps: [{ id: "run", is_result: true }], result_step: "run" },
  });
  assert.deepEqual(out.reply("g")?.["result"], {
    value: [
      { id: "test-linux", deps: ["targets"], work: { steps: [{ id: "run", is_result: true }], result_step: "run" } },
      { id: "test-darwin", deps: ["targets"], work: { steps: [{ id: "run", is_result: true }], result_step: "run" } },
    ],
  });
  assert.deepEqual(out.reply("n")?.["result"], { outcome: "success" });
  assert.ok(out.records().some((r) => r["node"] === "test-darwin" && r["msg"] === "testing darwin after targets"));
  assert.equal((out.reply("again")?.["error"] as { code: string }).code, "bad_request");
});

test("a generated node's own process reads the source's output through the routes", async () => {
  definePlatforms();
  const fake = (async (input: string | URL | Request) =>
    String(input) === "http://engine/node/v1/nodes/targets/output"
      ? new Response(JSON.stringify({ output: ["linux"] }))
      : new Response("", { status: 404 })) as typeof fetch;
  const out = await converse(
    [
      { id: "p", op: "plan", pipeline: "matrix", args: {}, run },
      { id: "g", op: "eval", closure: "targets/generator/0" },
      { id: "n", op: "run_node", node: "test-linux", attempt: 1 },
    ],
    new NodeRoutes({ baseUrl: "http://engine", token: "t", fetch: fake }),
  );
  assert.deepEqual(out.reply("g")?.["result"], {
    value: [{ id: "test-linux", deps: ["targets"], work: { steps: [{ id: "run", is_result: true }], result_step: "run" } }],
  });
  assert.deepEqual(out.reply("n")?.["result"], { outcome: "success" });

  const unreachable = await converse([
    { id: "p", op: "plan", pipeline: "matrix", args: {}, run },
    { id: "g", op: "eval", closure: "targets/generator/0" },
  ]);
  assert.deepEqual(unreachable.reply("g")?.["result"], {
    value: null,
    error: "the engine gave this process no node routes",
  });
});

test("run_node and run_step refuse an attempt that is not an integer of at least 1, without running the body", async () => {
  let calls = 0;
  definePipeline({ name: "counted", plan: (p) => void p.job("body", () => void calls++) });
  const reqs = [{}, { attempt: 0 }, { attempt: 1.5 }, { attempt: "oops" }].map((a, i) => ({
    id: `n${i}`,
    op: "run_node",
    node: "body",
    ...a,
  }));
  const stepped = { id: "s", op: "run_step", node: "deploy", step: "fetch", attempt: 0 };
  const out = await converse([{ id: "p", op: "plan", pipeline: "counted", args: {}, run }, ...reqs]);
  for (const r of reqs) assert.equal((out.reply(r.id)?.["error"] as { code: string }).code, "bad_request", r.id);
  assert.equal(calls, 0);
  const steps = await converse([plan, stepped]);
  assert.equal((steps.reply("s")?.["error"] as { code: string }).code, "bad_request");
});

test("a generator whose stepped source has no result step reads null in the source's process", async () => {
  let seen: unknown = "unset";
  definePipeline({
    name: "stepsource",
    plan(p) {
      const source = p.job("source");
      source.step("work", () => "not the node's output");
      source.generate((g, output) => {
        seen = output;
        g.job("child", () => undefined);
      });
    },
  });
  const notReady = (async () => new Response(JSON.stringify({ error: "running", code: "not_ready" }), { status: 409 })) as typeof fetch;
  const out = await converse(
    [
      { id: "p", op: "plan", pipeline: "stepsource", args: {}, run },
      { id: "s", op: "run_step", node: "source", step: "work", attempt: 1 },
      { id: "g", op: "eval", closure: "source/generator/0" },
    ],
    new NodeRoutes({ baseUrl: "http://engine", token: "t", fetch: notReady }),
  );
  assert.deepEqual(out.reply("s")?.["result"], { outcome: "success" });
  assert.deepEqual(out.reply("g")?.["result"], {
    value: [{ id: "child", deps: ["source"], work: { steps: [{ id: "run", is_result: true }], result_step: "run" } }],
  });
  assert.equal(seen, null);
});

test("cancelling a step aborts its pending route request", { timeout: 10_000 }, async () => {
  definePipeline({
    name: "slowsecret",
    plan: (p) =>
      void p
        .job("work")
        .step("fetch", (ctx) => ctx.secret("API_KEY"))
        .result(),
  });
  let arrived: () => void = () => undefined;
  const requestArrived = new Promise<void>((resolve) => (arrived = resolve));
  let peerClosed: () => void = () => undefined;
  const closed = new Promise<void>((resolve) => (peerClosed = resolve));
  const server = createServer((_req, res) => {
    res.on("close", peerClosed);
    arrived();
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    const c = new Conversation(new NodeRoutes({ baseUrl: `http://127.0.0.1:${(server.address() as AddressInfo).port}`, token: "t" }));
    await c.send({ id: "p", op: "plan", pipeline: "slowsecret", args: {}, run });
    c.write({ id: "s", op: "run_step", node: "work", step: "fetch", attempt: 1 });
    await requestArrived;
    await c.send({ id: "c", op: "cancel", node: "work", step: "fetch" });
    assert.equal(((await c.out.waitReply("s"))["result"] as { outcome: string }).outcome, "failed");
    await closed;
    await c.end();
  } finally {
    server.closeAllConnections();
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test("a dry run runs a step's dry-run body; an approval has no body; after_run needs an outcome; cache_key returns a string or null", async () => {
  definePipeline({
    name: "dsl",
    plan(p) {
      const j = p.job("j");
      j.step("apply", () => "applied")
        .dryRun(() => "planned")
        .result();
      p.approval("gate", { message: "ship?" });
      p.job("memo", () => undefined)
        .memoize((() => 7) as unknown as () => string)
        .afterRun(() => undefined);
    },
  });
  const dsl = { id: "p", op: "plan", pipeline: "dsl", args: {}, run: { ...run, dry_run: true } };
  const out = await converse([
    dsl,
    { id: "s", op: "run_step", node: "j", step: "apply", attempt: 1 },
    { id: "g", op: "run_node", node: "gate", attempt: 1 },
    { id: "a", op: "eval", closure: "memo/after_run/0", input: { outcome: "maybe" } },
    { id: "k", op: "eval", closure: "memo/cache_key/0" },
  ]);
  assert.deepEqual(out.reply("s")?.["result"], { outcome: "success", output: "planned" });
  assert.equal((out.reply("g")?.["error"] as { code: string }).code, "bad_request");
  assert.equal((out.reply("a")?.["error"] as { code: string }).code, "bad_request");
  assert.deepEqual(out.reply("k")?.["result"], {
    value: null,
    error: "cache_key closure memo/cache_key/0 returned number, not a string or null",
  });
});

test("a generated job's recovery closures answer eval; run_node refuses a node with several steps", async () => {
  definePipeline({
    name: "gen",
    plan(p) {
      p.job("src", () => ["a"]).generate<string[]>((gen, ids) => {
        for (const id of ids ?? []) {
          const child = gen.job(`child-${id}`, () => undefined).onFailure(`recover-${id}`, () => undefined);
          child.recovery?.beforeRun(() => undefined);
        }
      });
      const multi = p.job("multi");
      multi.step("a", () => undefined);
      multi.step("b", () => undefined);
    },
  });
  const out = await converse([
    { id: "p", op: "plan", pipeline: "gen", args: {}, run },
    { id: "s", op: "run_node", node: "src", attempt: 1 },
    { id: "g", op: "eval", closure: "src/generator/0" },
    { id: "b", op: "eval", closure: "recover-a/before_run/0" },
    { id: "m", op: "run_node", node: "multi", attempt: 1 },
  ]);
  const nodes = (out.reply("g")?.["result"] as { value: Array<{ id: string }> }).value;
  assert.deepEqual(
    nodes.map((n) => n.id),
    ["child-a", "recover-a"],
  );
  assert.deepEqual(out.reply("b")?.["result"], { value: null });
  assert.equal((out.reply("m")?.["error"] as { code: string }).code, "bad_request");
});
