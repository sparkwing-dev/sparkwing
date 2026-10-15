import assert from "node:assert/strict";
import { test } from "node:test";
import { definePipeline, resetPipelines, Plan } from "../src/plan.ts";
import { Host, hello } from "./helpers.ts";

async function fixture(build: (plan: Plan) => void): Promise<Host> {
  resetPipelines();
  definePipeline({ name: "settlement", plan: build });
  const host = new Host();
  await host.send({ ...hello, binding: { run_id: "r", job_id: "job", routes: { url: "http://127.0.0.1:1", bearer: "private" } } });
  assert.equal(
    (await host.send({ id: "plan", op: "plan", pipeline: "settlement", args: {}, run: { run_id: "r", pipeline: "alias" } })).ok,
    true,
  );
  return host;
}
const step = (id: string, name: string, attempt: number, settled: string[]) => ({
  id,
  op: "run_step",
  job: "job",
  step: name,
  attempt,
  settled,
});
const predicate = (id: string, attempt: number, settled: string[]) => ({
  id,
  op: "eval",
  closure: "job/step/consumer/skip_if/0",
  attempt,
  settled,
});

test("predicates and bodies see skipped predecessors and only the current attempt's output", async (t) => {
  for (const ordinal of [1, 2, 7, 103, 65536]) {
    const seen: unknown[] = [];
    const host = await fixture((p) => {
      const job = p.job("job");
      const source = job.step("source", () => ordinal);
      job
        .step("consumer", (ctx) => ctx.stepOutput(source) ?? "zero")
        .needs(source)
        .skipIf((ctx) => {
          seen.push(ctx.stepOutput(source));
          return false;
        });
    });
    try {
      assert.equal((await host.send(step("source", "source", ordinal, []))).result.output, ordinal);
      assert.equal((await host.send(predicate("same", ordinal, ["source"]))).result.value, false);
      assert.equal((await host.send(step("consumer", "consumer", ordinal, ["source"]))).result.output, ordinal);
      assert.equal((await host.send(predicate("next", ordinal + 1, ["source"]))).result.value, false);
      assert.deepEqual(seen, [ordinal, undefined]);
      assert.equal((await host.send(step("skipped", "consumer", ordinal + 1, ["source"]))).result.output, "zero");
    } finally {
      await host.close();
    }
  }
});

test("invalid snapshots fail before changing attempt state or invoking author code", async () => {
  let calls = 0;
  const host = await fixture((p) => {
    const job = p.job("job");
    const source = job.step("source", () => 42);
    const middle = job.step("middle", (ctx) => ctx.stepOutput(source)).needs(source);
    job
      .step("consumer", (ctx) => {
        calls++;
        return ctx.stepOutput(source);
      })
      .needs(middle)
      .skipIf(() => {
        calls++;
        return false;
      });
  });
  try {
    assert.equal((await host.send(step("source", "source", 1, []))).result.output, 42);
    const snapshots: unknown[] = [undefined, null, {}, ["missing"], ["source", "source"], ["consumer"], ["middle"]];
    for (const [index, settled] of snapshots.entries()) {
      const reply = await host.send({ ...predicate(`invalid-${index}`, 2, []), settled });
      assert.equal(reply.ok, false, JSON.stringify(reply));
    }
    assert.equal(calls, 0);
    const valid = await host.send(step("valid", "consumer", 1, ["source", "middle"]));
    assert.equal(valid.result.output, 42, JSON.stringify(valid));
  } finally {
    await host.close();
  }
});

test("terminal steps cannot execute or evaluate again until a later attempt", async () => {
  for (const failed of [false, true]) {
    let calls = 0;
    const host = await fixture((p) => {
      const job = p.job("job");
      const source = job.step("source", () => {
        calls++;
        if (failed) throw new Error("body failure");
        return 1;
      });
      job
        .step("consumer", () => null)
        .needs(source)
        .skipIf(() => false);
    });
    try {
      const first = await host.send(step("first", "source", 1, []));
      assert.equal(first.result.outcome, failed ? "failed" : "success");
      assert.equal((await host.send(step("again", "source", 1, []))).error?.code, "protocol_error");
      await host.send(step("consumer", "consumer", 1, ["source"]));
      assert.equal((await host.send(predicate("predicate", 1, ["source"]))).error?.code, "protocol_error");
      assert.equal((await host.send(step("next", "source", 2, []))).result.outcome, failed ? "failed" : "success");
      assert.equal(calls, 2);
    } finally {
      await host.close();
    }
  }
});

test("a snapshot cannot settle active work or advance an overlapping attempt", async () => {
  let enter!: () => void, release!: () => void;
  const entered = new Promise<void>((resolve) => {
    enter = resolve;
  });
  const barrier = new Promise<void>((resolve) => {
    release = resolve;
  });
  const host = await fixture((p) => {
    const job = p.job("job");
    const source = job.step("source", async () => {
      enter();
      await barrier;
      return 9;
    });
    job
      .step("consumer", () => null)
      .needs(source)
      .skipIf(() => false);
  });
  try {
    const pending = host.send(step("source", "source", 1, []));
    await entered;
    for (const attempt of [1, 2])
      assert.equal((await host.send(predicate(`overlap-${attempt}`, attempt, ["source"]))).error?.code, "protocol_error");
    release();
    await pending;
    assert.equal((await host.send(predicate("valid", 1, ["source"]))).result.value, false);
  } finally {
    release();
    await host.close();
  }
});

test("cancellation joins selected evals and targets request IDs without synthetic job fields", async () => {
  for (const selector of [{ job: "job" }, { target: "eval" }]) {
    let enter!: () => void, aborted!: () => void, release!: () => void;
    const entered = new Promise<void>((resolve) => {
      enter = resolve;
    });
    const stopped = new Promise<void>((resolve) => {
      aborted = resolve;
    });
    const barrier = new Promise<void>((resolve) => {
      release = resolve;
    });
    const host = await fixture((p) => {
      p.job("job", () => null).beforeRun(async (ctx) => {
        ctx.signal.addEventListener("abort", aborted, { once: true });
        enter();
        await barrier;
      });
    });
    try {
      const pending = host.send({ id: "eval", op: "eval", closure: "job/before_run/0" });
      await entered;
      const cancel = host.send({ id: "cancel", op: "cancel", ...selector });
      await stopped;
      assert.equal(
        host.frames.some((f) => f.reply === "cancel"),
        false,
      );
      release();
      assert.ok((await pending).result.error);
      assert.equal((await cancel).ok, true);
      assert.deepEqual(
        host.frames.filter((f) => ["eval", "cancel"].includes(f.reply)).map((f) => f.reply),
        ["eval", "cancel"],
      );
    } finally {
      release();
      await host.close();
    }
  }
});

test("dry runs require declared handling and settle failures without invoking real work", async () => {
  resetPipelines();
  let calls = 0;
  definePipeline({
    name: "dry",
    plan(p) {
      const job = p.job("job");
      job.step("unsafe", () => {
        calls++;
      });
      job
        .step("replacement", () => {
          calls++;
          return "real";
        })
        .dryRun(() => "dry");
      job
        .step("safe", () => {
          calls++;
          return "safe";
        })
        .safeWithoutDryRun();
    },
  });
  const host = new Host();
  try {
    await host.send({ ...hello, binding: { run_id: "r", job_id: "job", routes: { url: "http://127.0.0.1:1", bearer: "private" } } });
    await host.send({ id: "plan", op: "plan", pipeline: "dry", args: {}, run: { run_id: "r", pipeline: "dry", dry_run: true } });
    assert.equal((await host.send(step("unsafe", "unsafe", 1, []))).result.outcome, "failed");
    assert.equal((await host.send(step("again", "unsafe", 1, []))).error?.code, "protocol_error");
    assert.equal((await host.send(step("replacement", "replacement", 1, []))).result.output, "dry");
    assert.equal((await host.send(step("safe", "safe", 1, []))).result.output, "safe");
    assert.equal(calls, 1);
  } finally {
    await host.close();
  }
});
