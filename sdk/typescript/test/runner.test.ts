import assert from "node:assert/strict";
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
  });
  assert.deepEqual(doc.nodes[1], {
    id: "report",
    deps: [],
    needs_generated: ["targets/generator/0"],
  });
  assert.deepEqual(out.reply("g")?.["result"], {
    value: [
      { id: "test-linux", deps: ["targets"] },
      { id: "test-darwin", deps: ["targets"] },
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
    value: [{ id: "test-linux", deps: ["targets"] }],
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
