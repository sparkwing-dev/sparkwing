import assert from "node:assert/strict";
import { test } from "node:test";
import { definePipeline, resetPipelines } from "../src/plan.ts";
import { noMemo } from "../src/helpers/memo.ts";
import { SDKError } from "../src/errors.ts";
import { Host, hello } from "./helpers.ts";
import type { HostServices } from "../src/routes.ts";
import type { Context, Trigger } from "../index.d.ts";
import { MAX_FRAME_BYTES, MAX_OUTPUT_BYTES } from "../src/protocol.ts";
import { readFileSync } from "node:fs";

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

test("shared nested generation corpus preserves every source group", async () => {
  const vectors = JSON.parse(readFileSync(new URL("./fixtures/helper-vectors.json", import.meta.url), "utf8"));
  resetPipelines();
  definePipeline({
    name: "fixture",
    plan(plan) {
      const source = plan.job("source", () => null);
      plan.groupJobs("display", source);
      source.generate((generated) => {
        generated
          .job("child", () => null)
          .generate((nested) => {
            nested.job("grandchild", () => null);
          });
      });
    },
  });
  const host = await setup(undefined, "grandchild");
  try {
    const planned = await host.send(planRequest);
    const first = await host.send({ id: "first", op: "eval", closure: "source/generate/0", input: null });
    const second = await host.send({ id: "second", op: "eval", closure: "child/generate/0", input: null });
    const jobs = [...planned.result.jobs, ...first.result.value, ...second.result.value];
    assert.deepEqual(
      jobs.map((job) => ({ job: job.id, groups: [...job.groups].sort() })),
      vectors.groups,
    );
  } finally {
    await host.close();
  }
});

test("generated declarations use the frame budget while typed outputs keep their own budget", async () => {
  resetPipelines();
  definePipeline({
    name: "fixture",
    plan(plan) {
      plan
        .job("build", () => "x".repeat(MAX_OUTPUT_BYTES))
        .generate((generated) => {
          for (let n = 0; n < 256; n++) generated.job(`child-${n}`, () => null).env("VALUE", "x".repeat(64 << 10));
        });
    },
  });
  const host = await setup();
  try {
    const planned = await host.send(planRequest);
    assert.equal(planned.ok, true);
    const expanded = await host.send({ id: "expand", op: "eval", closure: planned.result.jobs[0].closures.generate[0], input: null });
    assert.equal(expanded.ok, true);
    assert.equal(expanded.result.error, undefined, JSON.stringify(expanded.result.error));
    assert.equal(expanded.result.value.length, 256);
    assert.ok(Buffer.byteLength(JSON.stringify(expanded)) > MAX_OUTPUT_BYTES);
    assert.ok(Buffer.byteLength(JSON.stringify(expanded)) < MAX_FRAME_BYTES);
    const executed = await host.send({ id: "execute", op: "run_step", job: "build", step: "run", attempt: 1, settled: [] });
    assert.equal(executed.result.outcome, "failed");
    assert.match(executed.result.error.message, /output exceeds/);
  } finally {
    await host.close();
  }
});

test("memo evaluation uses the wire budget rather than the typed-output budget", async () => {
  resetPipelines();
  definePipeline({
    name: "fixture",
    plan(plan) {
      plan.job("build", () => null).memo(() => "x".repeat(MAX_OUTPUT_BYTES + 1));
    },
  });
  const host = await setup();
  try {
    const planned = await host.send(planRequest);
    const result = await host.send({ id: "memo", op: "eval", closure: planned.result.jobs[0].closures.memo_key });
    assert.equal(result.result.error, undefined);
    assert.equal(result.result.value.length, MAX_OUTPUT_BYTES + 1);
    assert.ok(Buffer.byteLength(JSON.stringify(result)) < MAX_FRAME_BYTES);
  } finally {
    await host.close();
  }
});

test("duration arguments preserve signed zero in defaults and explicit inputs", async (t) => {
  for (const value of ["0", "+0", "-0"]) {
    await t.test(value, async () => {
      resetPipelines();
      definePipeline({
        name: "fixture",
        args: [{ name: "wait", type: "duration", default: value }],
        plan(plan) {
          plan
            .job("build", (ctx) => ({ pipeline: ctx.run.args.wait, job: ctx.args.jobWait }))
            .withArgs({ name: "jobWait", type: "duration", default: value });
        },
      });
      for (const args of [{ wait: value, jobWait: value }]) {
        const host = await setup();
        try {
          const described = await host.send({ id: "describe", op: "describe" });
          assert.equal(described.ok, true, JSON.stringify(described));
          assert.deepEqual(
            described.result.pipelines[0].args.map((arg: { default: string }) => arg.default),
            [value, value],
          );
          const planned = await host.send({ ...planRequest, args });
          assert.equal(planned.ok, true, JSON.stringify(planned));
          const executed = await host.send({ id: "step", op: "run_step", settled: [], job: "build", step: "run", attempt: 1 });
          assert.equal(executed.result.outcome, "success", JSON.stringify(executed));
          assert.deepEqual(executed.result.output, { pipeline: value, job: value });
        } finally {
          await host.close();
        }
      }
    });
  }
});

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
    const result = await host.send({ ...planRequest, args: { count: "9223372036854775807" } });
    assert.equal(result.ok, true, JSON.stringify(result));
    assert.equal(duringPlan, undefined);
    assert.deepEqual(Object.keys(result.result), ["jobs"]);
    const job = result.result.jobs[0];
    assert.equal(job.env.W5_BODY.value, "ready");
    assert.equal("modifiers" in job, false);
    const frame = await host.send({ id: "step", op: "run_step", settled: [], job: "build", step: job.steps[0].id, attempt: 1 });
    assert.deepEqual(frame.result.output, { env: "ready", args: { count: "9223372036854775807" }, identity: "granted" });
  } finally {
    await host.close();
  }
  assert.equal(process.env["W5_BODY"], undefined);
});

test("prototype-shaped body env reaches tools and restores the prior process env", async (t) => {
  const previous = Object.hasOwn(process.env, "__proto__") ? process.env["__proto__"] : undefined;
  t.after(() => {
    if (previous === undefined) delete process.env["__proto__"];
    else process.env["__proto__"] = previous;
  });
  delete process.env["__proto__"];
  resetPipelines();
  definePipeline({
    name: "fixture",
    plan(plan) {
      plan
        .job("build", async (ctx) => {
          const bodyValue = process.env["__proto__"];
          process.env["__proto__"] = "ambient-value";
          const toolValue = await ctx
            .exec([process.execPath, "-e", "require('node:fs').writeSync(1, JSON.stringify(process.env.__proto__))"])
            .json();
          return [bodyValue, toolValue];
        })
        .env("__proto__", "body-value");
    },
  });
  const host = await setup();
  try {
    const planned = await host.send(planRequest);
    assert.equal(planned.ok, true, JSON.stringify(planned));
    assert.deepEqual(planned.result.jobs[0].env, JSON.parse('{"__proto__":{"value":"body-value"}}'));
    const executed = await host.send({ id: "step", op: "run_step", settled: [], job: "build", step: "run", attempt: 1 });
    assert.equal(executed.result.outcome, "success", JSON.stringify(executed));
    assert.deepEqual(executed.result.output, ["body-value", "body-value"]);
  } finally {
    await host.close();
  }
  assert.equal(Object.hasOwn(process.env, "__proto__"), false);
});

test("trigger facts survive author mutations during planning and retry attempts", async (t) => {
  const cases = [
    { name: "manual", wire: undefined, expected: { source: "manual" } },
    {
      name: "pull request",
      wire: { source: "pull_request", user: "author", pull_request: { number: 7, base_sha: "base", head_sha: "head" } },
      expected: { source: "pull_request", user: "author", pullRequest: { number: 7, baseSHA: "base", headSHA: "head" } },
    },
  ];
  function overwrite(trigger: Trigger): void {
    Reflect.set(trigger, "source", "changed");
    Reflect.set(trigger, "user", "changed");
    if (trigger.pullRequest) {
      Reflect.set(trigger.pullRequest, "number", 99);
      Reflect.set(trigger.pullRequest, "headSHA", "changed");
    }
    Reflect.set(trigger, "pullRequest", { number: 100 });
  }
  for (const { name, wire, expected } of cases) {
    await t.test(name, async () => {
      resetPipelines();
      definePipeline({
        name: "fixture",
        plan(plan, run) {
          overwrite(run.trigger);
          plan.job("build", (ctx) => {
            const trigger = structuredClone(ctx.run.trigger);
            overwrite(ctx.run.trigger);
            return trigger;
          });
        },
      });
      const host = await setup();
      try {
        const planned = await host.send({ ...planRequest, run: { ...planRequest.run, ...(wire ? { trigger: wire } : {}) } });
        assert.equal(planned.ok, true, JSON.stringify(planned));
        const step = planned.result.jobs[0].steps[0].id;
        for (const attempt of [1, 2]) {
          const result = await host.send({ id: `attempt-${attempt}`, op: "run_step", settled: [], job: "build", step, attempt });
          assert.equal(result.result.outcome, "success", JSON.stringify(result));
          assert.deepEqual(result.result.output, expected);
        }
      } finally {
        await host.close();
      }
    });
  }
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
    const first = await host.send({ id: "first", op: "run_step", settled: [], job: "build", step: "first", attempt: 1 });
    assert.deepEqual(first.result.output, { value: 3 });
    const same = await host.send({ id: "same", op: "run_step", settled: ["first"], job: "build", step: "second", attempt: 1 });
    assert.deepEqual(same.result.output, { value: 3 });
    const retry = await host.send({ id: "retry", op: "run_step", settled: ["first"], job: "build", step: "second", attempt: 2 });
    assert.equal(retry.result.output, "unset");
    const bad = await host.send({ id: "bad", op: "run_step", settled: [], job: "build", step: "bad", attempt: 2 });
    assert.equal(bad.ok, true);
    assert.equal(bad.result.outcome, "failed");
    assert.equal(bad.result.error.code, "sdk_error");
  } finally {
    await host.close();
  }
});

test("job closures cannot carry attempt or step settlement fields", async () => {
  resetPipelines();
  definePipeline({
    name: "fixture",
    plan(p) {
      p.job("build", () => 1).beforeRun(() => {});
    },
  });
  const host = await setup();
  try {
    const plan = await host.send(planRequest);
    const reply = await host.send({ id: "hook", op: "eval", closure: plan.result.jobs[0].closures.before_run[0], attempt: 2, settled: [] });
    assert.equal(reply.error.code, "protocol_error");
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
    const pending = host.send({ id: "body", op: "run_step", settled: [], job: "build", step, attempt: 1 });
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
    const pending = host.send({ id: "run", op: "run_step", settled: [], job: "build", step: plan.result.jobs[0].steps[0].id, attempt: 1 });
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
          generated.job("generated", () => output).withArgs({ name: "token", type: "[]string", secret: true, default: "alpha-secret,beta-secret" });
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
      const frame = await host.send({
        id,
        op: "eval",
        closure,
        ...(id === "after" ? { input: { outcome: "failed", error: { code: "sdk_error", message: "failed" } } } : {}),
      });
      assert.equal(frame.result.value, null);
    }
    assert.deepEqual(called, ["before", "verify", "failed"]);
    const expansion = await host.send({ id: "generate", op: "eval", closure: closures.generate[0], input: "recorded memo output" });
    assert.equal(expansion.result.value[0].id, "generated");
    assert.equal(expansion.result.declarations_known, true);
    assert.deepEqual(expansion.result.arguments, [{ name: "token", type: "[]string", required: false, default: "alpha-secret,beta-secret", secret: true, job_id: "generated" }]);
    assert.ok(expansion.result.value[0].groups.includes(closures.generate[0]));
    const again = await host.send({ id: "again", op: "eval", closure: closures.generate[0], input: "changed" });
    assert.deepEqual(again.result.value, expansion.result.value);
  } finally {
    await host.close();
  }
});

test("cancelled closures retain a failed eval reply even when author code returns normally", async (t) => {
  for (const kind of ["skip_if", "before_run", "verify", "after_run", "memo_key", "generate"]) {
    await t.test(kind, async () => {
      resetPipelines();
      let started!: () => void, proceed!: () => void, stop!: () => void;
      const stopped = new Promise<void>((resolve) => {
        stop = resolve;
      });
      const entered = new Promise<void>((resolve) => {
        started = resolve;
      });
      const release = new Promise<void>((resolve) => {
        proceed = resolve;
      });
      const wait = async (ctx: Context) => {
        ctx.signal.addEventListener("abort", stop, { once: true });
        started();
        await release;
      };
      definePipeline({
        name: "fixture",
        plan(plan) {
          plan
            .job("build", () => null)
            .skipIf(async (ctx) => {
              await wait(ctx);
              return false;
            })
            .beforeRun(wait)
            .verify(wait)
            .afterRun(wait)
            .memo(async (ctx) => {
              await wait(ctx);
              return "key";
            })
            .generate(async (generated, _output, ctx) => {
              generated.job("child", () => null);
              await wait(ctx);
            });
        },
      });
      const host = await setup();
      try {
        const plan = await host.send(planRequest);
        const value = plan.result.jobs[0].closures[kind];
        const pending = host.send({
          id: "eval",
          op: "eval",
          closure: Array.isArray(value) ? value[0] : value,
          ...(kind === "after_run" ? { input: { outcome: "success" } } : kind === "generate" ? { input: null } : {}),
        });
        await entered;
        const cancellation = host.send({ id: "cancel", op: "cancel", job: "build" });
        await stopped;
        proceed();
        const cancelled = await cancellation;
        const evaluated = await pending;
        assert.equal(cancelled.ok, true);
        assert.equal(evaluated.ok, true);
        assert.equal(evaluated.result.value, null);
        assert.equal(evaluated.result.error?.code, "sdk_error");
        assert.match(evaluated.result.error.message, /cancelled by host/);
        assert.deepEqual(
          host.frames
            .filter((frame) => ["cancel", "eval"].includes(frame.reply))
            .map((frame) => frame.reply)
            .sort(),
          ["cancel", "eval"],
        );
      } finally {
        proceed();
        await host.close();
      }
    });
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
    const result = await host.send({
      id: "child",
      op: "run_step",
      settled: [],
      job: "child",
      step: expanded.result.value[0].steps[0].id,
      attempt: 7,
    });
    assert.deepEqual(result.result.output, { persisted: true });
  } finally {
    await host.close();
  }
});

test("cancelled generation leaves no reconstructed child code behind", async () => {
  resetPipelines();
  let ready!: () => void, proceed!: () => void, stop!: () => void;
  const stopped = new Promise<void>((resolve) => {
    stop = resolve;
  });
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
        .generate(async (generated, _output, ctx) => {
          ctx.signal.addEventListener("abort", stop, { once: true });
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
    const cancellation = host.send({ id: "cancel", op: "cancel", job: "build" });
    await stopped;
    proceed();
    await cancellation;
    assert.equal((await pending).result.error.code, "sdk_error");
    const child = await host.send({ id: "child", op: "run_step", settled: [], job: "child", step: "body", attempt: 1 });
    assert.equal(child.error.code, "unknown_job");
  } finally {
    proceed();
    await host.close();
  }
});

test("execution cannot mutate topology and malformed host arguments are refused", async () => {
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
      settled: [],
      job: "build",
      step: plan.result.jobs[0].steps[0].id,
      attempt: 1,
      args: { mode: 1 },
    });
    assert.equal(invalid.error.code, "bad_request");
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
    assert.equal((await host.send(planRequest)).error.code, "protocol_error");
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
    const result = await host.send({
      id: "child",
      op: "run_step",
      settled: [],
      job: "child",
      step: expanded.result.value[0].steps[0].id,
      attempt: 1,
    });
    assert.equal(result.result.output, "absent");
  } finally {
    await host.close();
  }
});

test("malformed host arguments and after-run outcomes are refused", async () => {
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
      settled: [],
      job: "build",
      step: job.steps[0].id,
      attempt: 1,
      args: { undeclared: null },
    });
    assert.equal(invalid.error.code, "bad_request");
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
      const result = await host.send({
        id: "output",
        op: "run_step",
        settled: [],
        job: "build",
        step: plan.result.jobs[0].steps[0].id,
        attempt: 1,
      });
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
  void host.send({ id: "one", op: "run_step", settled: [], job: "build", step: "one", attempt: 1 });
  void host.send({ id: "two", op: "run_step", settled: [], job: "build", step: "two", attempt: 1 });
  await started;
  const before = host.frames.length;
  await host.close();
  await aborted;
  assert.equal(host.frames.length, before);
});

test("generated argument declarations rely on host preparation", async () => {
  resetPipelines();
  definePipeline({
    name: "fixture",
    plan(p) {
      p.job("build", () => null).generate((g) => {
        g.job("child", (ctx) => ctx.args.required).withArgs({ name: "required", type: "string", required: true });
      });
    },
  });
  const host = await setup(undefined, "child");
  try {
    const planned = await host.send(planRequest);
    const expanded = await host.send({ id: "expand", op: "eval", closure: planned.result.jobs[0].closures.generate[0], input: null });
    assert.equal(expanded.result.error, undefined);
    assert.deepEqual(expanded.result.value[0].arguments, [{ name: "required", type: "string", required: true, job_id: "child" }]);
    const child = await host.send({ id: "child", op: "run_step", args: { required: "prepared-by-host" }, job: "child", step: "run", attempt: 1, settled: [] });
    assert.equal(child.result.outcome, "success");
    assert.equal(child.result.output, "prepared-by-host");
  } finally {
    await host.close();
  }
});

test("declared secrets do not call Mask during planning", async () => {
  resetPipelines();
  let calls = 0;
  definePipeline({
    name: "fixture",
    args: [{ name: "tokens", type: "[]string", secret: true, default: "default-private,supplied-private" }],
    plan(plan) {
      plan.job("build", () => null).withArgs({ name: "extra", type: "[]string", secret: true, default: "job-private-value" });
    },
  });
  const host = await setup(fakeServices({ mask: async () => { calls++; } }));
  try {
    const reply = await host.send({ ...planRequest, args: { tokens: "supplied-private" } });
    assert.equal(reply.ok, true, JSON.stringify(reply));
    assert.equal(calls, 0);
  } finally {
    await host.close();
  }
});

test("standalone generator returns author error for host sanitization", async () => {
  resetPipelines();
  definePipeline({
    name: "fixture",
    args: [{ name: "token", type: "string", secret: true, default: "private-code" }],
    plan(plan) {
      plan
        .job("build", () => null)
        .generate(() => {
          throw new SDKError("failed", { code: "failure-private-code" });
        });
    },
  });
  const host = await setup();
  try {
    const planned = await host.send(planRequest);
    const reply = await host.send({ id: "generate", op: "eval", closure: planned.result.jobs[0].closures.generate[0], input: null });
    assert.equal(reply.result.error.code, "failure-private-code");
  } finally {
    await host.close();
  }
});
