import assert from "node:assert/strict";
import { test } from "node:test";
import { definePipeline, resetPipelines } from "../src/plan.ts";
import { noMemo } from "../src/helpers/memo.ts";
import { SDKError } from "../src/errors.ts";
import { Host, hello } from "./helpers.ts";
import type { HostServices } from "../src/routes.ts";
import type { Context } from "../index.d.ts";

const binding = { run_id: "r1", job_id: "build", routes: { url: "http://127.0.0.1:12345", bearer: "hello-only-bearer" } };
const planRequest = {
  id: "plan",
  op: "plan",
  pipeline: "fixture",
  args: {},
  run: {
    run_id: "r1",
    pipeline: "yaml-alias",
    git: { sha: "sealed-sha", branch: "branch", repo: "owner/repo" },
    dry_run: false,
    no_memo: true,
  },
};
async function setup(services?: HostServices, job = "build") {
  const host = new Host(services ? { services: () => services } : {});
  assert.equal((await host.send({ ...hello, binding: { ...binding, job_id: job } })).ok, true);
  return host;
}
function fakeServices(overrides: Partial<HostServices> = {}): HostServices {
  const unavailable = async (): Promise<never> => {
    throw new Error("unexpected host service");
  };
  return {
    secret: unavailable,
    output: unavailable,
    latest: unavailable,
    annotate: unavailable,
    summary: unavailable,
    mask: unavailable,
    oidcToken: unavailable,
    runPipeline: unavailable,
    cleanup: unavailable,
    ...overrides,
  };
}

test("hello facts remain sealed, plans stay flat, body env starts only after planning", async () => {
  resetPipelines();
  let duringPlan: string | undefined;
  definePipeline({
    name: "fixture",
    plan(plan, run) {
      duringPlan = process.env["W5_BODY"];
      plan
        .job("build", (ctx) => {
          assert.equal(ctx.run.pipeline, "yaml-alias");
          assert.equal(ctx.runner.name, "granted");
          assert.equal(ctx.run.git.commit, "sealed-sha");
          assert.equal(ctx.run.noMemo, true);
          assert.equal("bearer" in ctx, false);
          assert.equal("attempt" in ctx, false);
          return { env: process.env["W5_BODY"], args: ctx.args, identity: run.runner.name };
        })
        .env("W5_BODY", "ready")
        .withArgs({ name: "count", type: "int64", default: "9223372036854775807" });
    },
  });
  const host = await setup();
  try {
    const result = await host.send(planRequest);
    assert.equal(result.ok, true, JSON.stringify(result));
    assert.equal(duringPlan, undefined);
    assert.deepEqual(Object.keys(result.result), ["jobs"]);
    const job = result.result.jobs[0];
    assert.equal(job.env.W5_BODY.value, "ready");
    assert.equal("modifiers" in job, false);
    const frame = await host.send({ id: "step", op: "run_step", job: "build", step: job.steps[0].id, attempt: 1 });
    assert.deepEqual(frame.result.output, { env: "ready", args: { count: "9223372036854775807" }, identity: "granted" });
  } finally {
    await host.close();
  }
  assert.equal(process.env["W5_BODY"], undefined);
});

test("outputs are per attempt, all step outputs are returned, bad JSON fails loudly", async () => {
  resetPipelines();
  definePipeline({
    name: "fixture",
    plan(plan) {
      const job = plan.job("build");
      const first = job.step("first", () => ({ value: 3 }));
      job
        .step("second", (ctx) => ctx.stepOutput(first) ?? "unset")
        .needs(first)
        .result();
      job.step("bad", () => 1n);
    },
  });
  const host = await setup();
  try {
    await host.send(planRequest);
    const first = await host.send({ id: "first", op: "run_step", job: "build", step: "first", attempt: 1 });
    assert.deepEqual(first.result.output, { value: 3 });
    const same = await host.send({ id: "same", op: "run_step", job: "build", step: "second", attempt: 1 });
    assert.deepEqual(same.result.output, { value: 3 });
    const retry = await host.send({ id: "retry", op: "run_step", job: "build", step: "second", attempt: 2 });
    assert.equal(retry.result.output, "unset");
    const bad = await host.send({ id: "bad", op: "run_step", job: "build", step: "bad", attempt: 2 });
    assert.equal(bad.ok, true);
    assert.equal(bad.result.outcome, "failed");
    assert.equal(bad.result.error.code, "sdk_error");
  } finally {
    await host.close();
  }
});

test("cancel acknowledges separately and the in-flight body retains its reply", async () => {
  resetPipelines();
  let started!: () => void;
  const barrier = new Promise<void>((resolve) => {
    started = resolve;
  });
  definePipeline({
    name: "fixture",
    plan(plan) {
      plan.job("build", async (ctx) => {
        started();
        await new Promise<void>((_resolve, reject) =>
          ctx.signal.addEventListener("abort", () => reject(ctx.signal.reason), { once: true }),
        );
      });
    },
  });
  const host = await setup();
  try {
    const plan = await host.send(planRequest);
    const step = plan.result.jobs[0].steps[0].id;
    const pending = host.send({ id: "body", op: "run_step", job: "build", step, attempt: 1 });
    await barrier;
    assert.equal((await host.send({ id: "cancel", op: "cancel", job: "build", step })).ok, true);
    const body = await pending;
    assert.equal(body.ok, true);
    assert.equal(body.result.outcome, "failed");
  } finally {
    await host.close();
  }
});

test("secrets are lazy and declared, optional absence is narrow, masks are acknowledged", async () => {
  resetPipelines();
  const calls: string[] = [];
  let release!: () => void;
  let masked = false;
  let enteredMask!: () => void;
  const maskReached = new Promise<void>((resolve) => {
    enteredMask = resolve;
  });
  const barrier = new Promise<void>((resolve) => {
    release = resolve;
  });
  definePipeline({
    name: "fixture",
    secrets: [{ name: "KEY" }, { name: "ABSENT", optional: true }, { name: "REGION", variable: true }],
    plan(plan) {
      plan.job("build", async (ctx) => {
        assert.equal(await ctx.secret("KEY"), "value");
        assert.equal(await ctx.variable("REGION"), "west");
        assert.equal(await ctx.secret("ABSENT"), undefined);
        await assert.rejects(ctx.secret("OTHER"), (e: any) => e.code === "forbidden" && e.details.reason === "secret_undeclared");
        await ctx.mask("dynamic");
        assert.equal(masked, true);
        ctx.log.info("after mask");
        await ctx.annotate({ message: "diagnostic", file: "src.ts", line: 1, endLine: 2, column: 4 });
        await ctx.summary("summary");
        return "done";
      });
    },
  });
  const services = fakeServices({
    async secret(name) {
      calls.push(name);
      if (name === "ABSENT") throw new SDKError("missing", { code: "not_found" });
      return name === "KEY" ? "value" : "west";
    },
    async mask() {
      enteredMask();
      await barrier;
      masked = true;
    },
    async annotate(value) {
      assert.equal(value.endLine, 2);
      calls.push("annotation");
    },
    async summary(value) {
      assert.equal(value, "summary");
      calls.push("summary");
    },
  });
  const host = await setup(services);
  try {
    const plan = await host.send(planRequest);
    assert.deepEqual(calls, []);
    const pending = host.send({ id: "run", op: "run_step", job: "build", step: plan.result.jobs[0].steps[0].id, attempt: 1 });
    await maskReached;
    assert.equal(
      host.frames.some((frame) => frame.msg === "after mask"),
      false,
    );
    release();
    const result = await pending;
    assert.equal(result.result.output, "done");
    assert.deepEqual(calls, ["KEY", "REGION", "ABSENT", "annotation", "summary"]);
    const record = host.frames.find((frame) => frame.msg === "after mask");
    assert.ok(record);
    assert.equal("job" in record, false);
    assert.equal("node" in record, false);
  } finally {
    await host.close();
  }
});

test("eval supports predicates, hooks, noMemo and generation from recorded output", async () => {
  resetPipelines();
  const called: string[] = [];
  definePipeline({
    name: "fixture",
    plan(plan) {
      plan
        .job("build", () => "body must not run")
        .skipIf(() => true)
        .beforeRun(() => {
          called.push("before");
        })
        .verify(() => {
          called.push("verify");
        })
        .afterRun((_ctx, result) => {
          called.push(result.outcome);
        })
        .memo(() => noMemo)
        .generate((generated, output) => {
          generated.job("generated", () => output);
        });
    },
  });
  const host = await setup();
  try {
    const plan = await host.send(planRequest);
    const closures = plan.result.jobs[0].closures;
    assert.equal((await host.send({ id: "skip", op: "eval", closure: closures.skip_if[0] })).result.value, true);
    assert.equal((await host.send({ id: "memo", op: "eval", closure: closures.memo_key })).result.value, null);
    for (const [id, closure] of [
      ["before", closures.before_run[0]],
      ["verify", closures.verify],
      ["after", closures.after_run[0]],
    ]) {
      const frame = await host.send({ id, op: "eval", closure, ...(id === "after" ? { input: { outcome: "failed" } } : {}) });
      assert.equal(frame.result.value, null);
    }
    assert.deepEqual(called, ["before", "verify", "failed"]);
    const expansion = await host.send({ id: "generate", op: "eval", closure: closures.generate[0], input: "recorded memo output" });
    assert.equal(expansion.result.value[0].id, "generated");
    assert.ok(expansion.result.value[0].groups.includes(closures.generate[0]));
    const again = await host.send({ id: "again", op: "eval", closure: closures.generate[0], input: "changed" });
    assert.equal(again.result.error.code, "bad_request");
  } finally {
    await host.close();
  }
});

test("a generated-job process reconstructs from recorded source output without rerunning its source", async () => {
  resetPipelines();
  definePipeline({
    name: "fixture",
    plan(plan) {
      plan
        .job("build", () => {
          throw new Error("source must never rerun");
        })
        .generate((generated, output) => {
          generated.job("child", () => output);
        });
    },
  });
  const host = await setup(undefined, "child");
  try {
    const plan = await host.send(planRequest);
    const expanded = await host.send({
      id: "expand",
      op: "eval",
      closure: plan.result.jobs[0].closures.generate[0],
      input: { persisted: true },
    });
    assert.equal(expanded.ok, true, JSON.stringify(expanded));
    assert.equal(expanded.result.error, undefined);
    const result = await host.send({ id: "child", op: "run_step", job: "child", step: expanded.result.value[0].steps[0].id, attempt: 7 });
    assert.deepEqual(result.result.output, { persisted: true });
  } finally {
    await host.close();
  }
});

test("cancelled generation leaves no reconstructed child code behind", async () => {
  resetPipelines();
  let ready!: () => void, proceed!: () => void;
  const entered = new Promise<void>((resolve) => {
    ready = resolve;
  });
  const barrier = new Promise<void>((resolve) => {
    proceed = resolve;
  });
  definePipeline({
    name: "fixture",
    plan(plan) {
      plan
        .job("build", () => null)
        .generate(async (generated) => {
          generated.job("child", () => true);
          ready();
          await barrier;
        });
    },
  });
  const host = await setup();
  try {
    const plan = await host.send(planRequest);
    const pending = host.send({ id: "expand", op: "eval", closure: plan.result.jobs[0].closures.generate[0], input: null });
    await entered;
    await host.send({ id: "cancel", op: "cancel", job: "build" });
    proceed();
    assert.equal((await pending).result.error.code, "sdk_error");
    const child = await host.send({ id: "child", op: "run_step", job: "child", step: "body", attempt: 1 });
    assert.equal(child.error.code, "unknown_job");
  } finally {
    proceed();
    await host.close();
  }
});

test("execution cannot mutate topology and argument failures use invalid_args", async () => {
  resetPipelines();
  definePipeline({
    name: "fixture",
    args: [{ name: "mode", type: "string", enum: ["safe"], default: "safe" }],
    plan(plan) {
      plan
        .job("build", () => true)
        .skipIf(() => {
          plan.job("injected", () => 1);
          return false;
        });
    },
  });
  const host = await setup();
  try {
    const plan = await host.send(planRequest);
    const evalResult = await host.send({ id: "eval", op: "eval", closure: plan.result.jobs[0].closures.skip_if[0] });
    assert.equal(evalResult.result.error.code, "plan_drift");
    const invalid = await host.send({
      id: "run",
      op: "run_step",
      job: "build",
      step: plan.result.jobs[0].steps[0].id,
      attempt: 1,
      args: { mode: "invalid" },
    });
    assert.equal(invalid.error.code, "invalid_args");
  } finally {
    await host.close();
  }
});

test("a failed hello cannot authorize planning", async () => {
  resetPipelines();
  let planned = false;
  definePipeline({
    name: "fixture",
    plan(plan) {
      planned = true;
      plan.job("build", () => 1);
    },
  });
  const host = new Host();
  try {
    const rejected = await host.send({ ...hello, binding: { ...binding, routes: { url: "https://outside.example", bearer: "private" } } });
    assert.equal(rejected.error.code, "bad_request");
    assert.equal((await host.send(planRequest)).error.code, "bad_request");
    assert.equal(planned, false);
  } finally {
    await host.close();
  }
});

test("generator source body env does not leak into the reconstructed child", async () => {
  resetPipelines();
  definePipeline({
    name: "fixture",
    plan(plan) {
      plan
        .job("build", () => null)
        .env("W5_SOURCE_ONLY", "source")
        .generate((generated) => {
          generated.job("child", () => process.env["W5_SOURCE_ONLY"] ?? "absent");
        });
    },
  });
  const host = await setup(undefined, "child");
  try {
    const plan = await host.send(planRequest);
    const expanded = await host.send({ id: "expand", op: "eval", closure: plan.result.jobs[0].closures.generate[0], input: null });
    const result = await host.send({ id: "child", op: "run_step", job: "child", step: expanded.result.value[0].steps[0].id, attempt: 1 });
    assert.equal(result.result.output, "absent");
  } finally {
    await host.close();
  }
});

test("unknown execution args and malformed after-run outcomes are refused", async () => {
  resetPipelines();
  let observed: unknown;
  definePipeline({
    name: "fixture",
    plan(plan) {
      plan
        .job("build", () => 1)
        .afterRun((_ctx, result) => {
          observed = result.failure;
        });
    },
  });
  const host = await setup();
  try {
    const plan = await host.send(planRequest);
    const job = plan.result.jobs[0];
    const invalid = await host.send({
      id: "args",
      op: "run_step",
      job: "build",
      step: job.steps[0].id,
      attempt: 1,
      args: { undeclared: "value" },
    });
    assert.equal(invalid.error.code, "invalid_args");
    const wrong = await host.send({ id: "outcome", op: "eval", closure: job.closures.after_run[0], input: { outcome: ["success"] } });
    assert.equal(wrong.result.error.code, "bad_request");
    const failed = await host.send({
      id: "failed",
      op: "eval",
      closure: job.closures.after_run[0],
      input: { outcome: "failed", error: { code: "sdk_error", message: "body failed" } },
    });
    assert.equal(failed.result.error, undefined);
    assert.deepEqual(observed, { job: "build", stage: "action", message: "body failed" });
  } finally {
    await host.close();
  }
});

test("output at 16 MiB passes and the next byte fails", async () => {
  for (const excess of [0, 1]) {
    resetPipelines();
    definePipeline({
      name: "fixture",
      plan(plan) {
        plan.job("build", () => "x".repeat((16 << 20) - 2 + excess));
      },
    });
    const host = await setup();
    try {
      const plan = await host.send(planRequest);
      const result = await host.send({ id: "output", op: "run_step", job: "build", step: plan.result.jobs[0].steps[0].id, attempt: 1 });
      assert.equal(result.result.outcome, excess ? "failed" : "success");
      if (!excess) assert.equal(Buffer.byteLength(JSON.stringify(result.result.output)), 16 << 20);
      else assert.equal(result.result.error.code, "sdk_error");
    } finally {
      await host.close();
    }
  }
});

test("independent steps run concurrently and EOF aborts them without late frames", async () => {
  resetPipelines();
  let bothStarted!: () => void, bothAborted!: () => void;
  const started = new Promise<void>((resolve) => {
    bothStarted = resolve;
  });
  const aborted = new Promise<void>((resolve) => {
    bothAborted = resolve;
  });
  let active = 0,
    stopped = 0;
  definePipeline({
    name: "fixture",
    plan(plan) {
      const job = plan.job("build");
      for (const id of ["one", "two"])
        job.step(id, async (ctx) => {
          if (++active === 2) bothStarted();
          await new Promise<void>((resolve) => ctx.signal.addEventListener("abort", () => resolve(), { once: true }));
          ctx.log.info("late log");
          if (++stopped === 2) bothAborted();
        });
    },
  });
  const host = await setup();
  await host.send(planRequest);
  void host.send({ id: "one", op: "run_step", job: "build", step: "one", attempt: 1 });
  void host.send({ id: "two", op: "run_step", job: "build", step: "two", attempt: 1 });
  await started;
  const before = host.frames.length;
  await host.close();
  await aborted;
  assert.equal(host.frames.length, before);
});
