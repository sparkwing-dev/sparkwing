import assert from "node:assert/strict";
import { test } from "node:test";
import type { Context } from "../index.d.ts";
import { ConcurrencyGroup, Generated, Plan, dir, goModules, npm, refTo, refToLastRun, startup } from "../src/plan.ts";
import { SDKError } from "../src/errors.ts";

test("flat declarations keep zero and false settings and leave authority to the host", () => {
  const plan = new Plan()
    .priority(0)
    .claimWait(0)
    .admissionClass("default")
    .checkout({ depth: 0, fullHistory: false, tags: false, submodules: false, lfs: false });
  const gate = plan.approval("gate", { timeoutMs: 0, onTimeout: "deny" }).skipIf(() => false, { budgetMs: 0 });
  const build = plan
    .job("build", () => 1)
    .needs(gate)
    .retry(0, { backoffMs: 0 })
    .timeout(0)
    .noProgressTimeout(0)
    .memo(() => "key", { ttlMs: 0 })
    .env("CI", "1")
    .env("NODE_OPTIONS", "--stack-trace-limit=50", startup());
  build.cache(goModules(), npm(), dir("./web/node_modules/", "web/package-lock.json"));
  const capacity = new ConcurrencyGroup("builds", { scope: "box", capacity: 2, queueTimeoutMs: 0 });
  plan.concurrency(capacity);
  build.concurrency(capacity, 2);
  const doc = plan.toDoc();
  assert.deepEqual(Object.keys(doc).sort(), ["admission_class", "claim_wait_ms", "concurrency", "jobs", "priority", "source"]);
  assert.deepEqual(doc.source, { depth: 0, full_history: false, tags: false, submodules: false, lfs: false });
  assert.deepEqual(doc.concurrency, [{ group: "builds", scope: "box", capacity: 2, queue_timeout_ms: 0 }]);
  assert.deepEqual(doc.jobs[0], {
    id: "gate",
    approval: { timeout_ms: 0, on_timeout: "deny" },
    skip_if_budget_ms: 0,
    closures: { skip_if: ["gate/skip_if/0"] },
  });
  const job = doc.jobs[1]!;
  assert.deepEqual(job.retry, { attempts: 0, backoff_ms: 0 });
  assert.equal(job.timeout_ms, 0);
  assert.equal(job.no_progress_timeout_ms, 0);
  assert.deepEqual(job.memo, { ttl_ms: 0 });
  assert.deepEqual(job.env, { CI: { value: "1" }, NODE_OPTIONS: { value: "--stack-trace-limit=50", startup: true } });
  assert.deepEqual(job.caches, [
    { name: "go-modules", resolver: "gomod", key_files: ["go.sum"] },
    { name: "npm", resolver: "npm", key_files: ["package-lock.json"] },
    { name: "node_modules", path: "./web/node_modules/", key_scope: "web/node_modules", key_files: ["web/package-lock.json"] },
  ]);
  assert.deepEqual(job.steps, [{ id: "run" }]);
  assert.equal(job.result_step, "run");
});

test("groups expand dependencies and modify their own members", () => {
  const plan = new Plan();
  const a = plan.job("a", () => 1);
  const b = plan.job("b", () => 2);
  const group = plan.groupJobs("checks", a, b).retry(2).optional().needsTools("git", "git").requires("os=linux");
  const consumer = plan.job("consumer").needs(group).needsIfPresent("omitted").consumes(group, { into: "out" });
  const s1 = consumer.step("one", () => 1);
  const s2 = consumer.step("two", () => 2);
  const steps = consumer.groupSteps("pair", s1, s2).skipIf(() => false);
  consumer
    .step("end", () => 3)
    .needs(steps)
    .result()
    .risk("prod")
    .optional()
    .finally()
    .safeWithoutDryRun();
  consumer.failurePolicy("collect_all");
  const doc = plan.toDoc();
  assert.deepEqual(doc.jobs[0]?.requires, ["os=linux", "tool:git"]);
  assert.deepEqual(doc.jobs[1]?.groups, ["checks"]);
  assert.equal(doc.jobs[0]?.optional, true);
  assert.equal(doc.jobs[0]?.continue_on_error, true);
  assert.deepEqual(doc.jobs[2]?.deps, ["a", "b"]);
  assert.deepEqual(doc.jobs[2]?.deps_if_present, ["omitted"]);
  assert.deepEqual(doc.jobs[2]?.consumes, [
    { producer: "a", into: "out" },
    { producer: "b", into: "out" },
  ]);
  assert.deepEqual(doc.jobs[2]?.steps?.[0]?.groups, ["pair"]);
  assert.deepEqual(doc.jobs[2]?.steps?.[2], {
    id: "end",
    needs: ["one", "two"],
    risks: ["prod"],
    optional: true,
    continue_on_error: true,
    finally: true,
    safe_without_dry_run: true,
  });
});

test("closures locate the declared bodies, hooks and typed recovery jobs", async () => {
  const plan = new Plan();
  const events: string[] = [];
  const job = plan
    .job("work")
    .skipIf(() => false)
    .beforeRun(() => {
      events.push("before");
    })
    .verify(() => {
      events.push("verify");
    })
    .afterRun((_ctx, result) => {
      events.push(result.outcome);
    })
    .memo(() => "memo");
  const step = job
    .step("s", () => 1)
    .dryRun(() => 2)
    .result()
    .skipIf(() => true);
  const recovery = job.onFailure("recover", () => 3).retry(1);
  const approvalRecovery = plan.approval("approve").onFailure("approve-recover", () => 4);
  assert.equal(recovery.id, "recover");
  assert.equal(approvalRecovery.id, "approve-recover");
  const doc = plan.toDoc();
  assert.equal(doc.jobs.find((j) => j.id === "recover")?.on_failure_of, "work");
  assert.deepEqual(
    job
      .closures()
      .map((c) => c.kind)
      .sort(),
    ["after_run", "before_run", "memo_key", "skip_if", "skip_if", "verify"],
  );
  assert.equal(plan.findClosure("work/step/s/skip_if/0")?.step, step);
  assert.equal(plan.findJob("work")?.findStep("s"), step);
  const ctx = {} as Context;
  assert.equal(await step.body(ctx), 1);
  assert.equal(await step.dryRunBody?.(ctx), 2);
  for (const closure of job.closures()) {
    if (closure.kind === "before_run" || closure.kind === "verify") await closure.fn(ctx);
    if (closure.kind === "after_run") await closure.fn(ctx, { outcome: "failed" });
  }
  assert.deepEqual(events, ["before", "verify", "failed"]);
});

test("generators label expansions and insert replay code atomically", async () => {
  const plan = new Plan();
  const source = plan.job("source", () => ({ values: [1, 2] }));
  const generator = source.generate((generated, output) => {
    for (const n of output.values) generated.job(`item-${n}`, () => n);
  });
  plan.job("join", () => undefined).needsGenerated(generator);
  const closure = plan.findClosure(generator.id);
  assert.equal(closure?.kind, "generate");
  if (closure?.kind !== "generate") throw new Error("missing generator closure");
  const generated = new Generated(source, generator.id, plan);
  await closure.fn(generated, { values: [3] }, {} as Context);
  assert.deepEqual(generated.toDoc().jobs, [
    { id: "item-3", deps: ["source"], groups: [generator.id], steps: [{ id: "run" }], result_step: "run" },
  ]);
  plan.insert(generated);
  assert.equal(
    await plan
      .findJob("item-3")
      ?.findStep("run")
      ?.body({} as Context),
    3,
  );

});

test("builder forwards graph declarations and preserves host-owned ceilings", () => {
  const cycle = new Plan();
  cycle.job("a", () => undefined).needs("b");
  cycle.job("b", () => undefined).needs("a");
  assert.equal(cycle.toDoc().jobs.length, 2);
  const absent = new Plan();
  absent.job("a", () => undefined).needs("ghost");
  assert.equal(absent.toDoc().jobs.length, 1);
  const p = new Plan();
  const j = p.job("j");
  j.step("a", () => undefined)
    .needs("b")
    .result();
  j.step("b", () => undefined)
    .needs("a")
    .result();
  assert.throws(() => p.toDoc(), /result step/);
  assert.throws(() => p.job("j"), /duplicate job/);
  assert.throws(() => j.retry(-1), /nonnegative integer/);
  assert.throws(() => j.timeout(1.1), /nonnegative integer/);
  assert.throws(() => j.needsTools("typo"), /known tool/);
  const limits = new Plan();
  limits
    .job("j", () => undefined)
    .retry(11)
    .memo(() => "key", { ttlMs: 31 * 86400000 });
  assert.equal(limits.toDoc().jobs[0]?.retry?.attempts, 11);
  assert.equal(limits.toDoc().jobs[0]?.memo?.ttl_ms, 31 * 86400000);
});


test("refs retain edges, max age and absence behavior", async () => {
  const plan = new Plan();
  const producer = plan.job("producer", () => 42);
  const direct = refTo(producer);
  const previous = refToLastRun<number>("build", "producer", { maxAgeMs: 0 });
  plan.job("consumer", () => undefined).references(previous);
  assert.deepEqual(plan.toDoc().jobs[1]?.pipeline_refs, [{ pipeline: "build", job: "producer" }]);
  assert.equal(previous.maxAgeMs, 0);
  assert.equal(await direct.get({ output: async () => 42 } as unknown as Context), 42);
  assert.equal(await direct.tryGet({ output: async () => undefined } as unknown as Context), undefined);
  assert.equal(
    await previous.tryGet({
      latest: async () => {
        throw new SDKError("absent", { code: "not_found" });
      },
    } as unknown as Context),
    undefined,
  );
  const forbidden = Object.assign(new Error("forbidden"), { code: "forbidden" });
  await assert.rejects(
    previous.tryGet({
      latest: async () => {
        throw forbidden;
      },
    } as unknown as Context),
    forbidden,
  );
});

test("graph declarations reach the host while cross-plan groups fail locally", () => {
  const plan = new Plan();
  const job = plan.job("job");
  job.step("one", () => undefined).needs("two");
  job.step("two", () => undefined).needs("one");
  assert.equal(plan.toDoc().jobs.length, 1);
  const elsewhere = new Plan().job("elsewhere", () => undefined);
  assert.throws(() => plan.groupJobs("other", elsewhere), /another plan/);
  const otherStep = new Plan().job("other").step("elsewhere", () => undefined);
  assert.throws(() => job.groupSteps("other", otherStep), /another job/);
  const waiting = new Plan();
  waiting.job("wait", () => undefined).needsGenerated(elsewhere.generate(() => undefined));
  assert.equal(waiting.toDoc().jobs.length, 1);
});

test("argument declarations reach the host and wire copies do not mutate declarations", () => {
  const plan = new Plan();
  plan.job("first", () => undefined).withArgs({ name: "arg", type: "string" });
  plan.job("second", () => undefined).withArgs({ name: "arg", type: "string" });
  assert.equal(plan.toDoc().jobs.length, 2);
  const single = new Plan();
  const j = single
    .job("work", () => undefined)
    .env("KEY", "original")
    .artifacts("out/*");
  const doc = single.toDoc();
  doc.jobs[0]!.env!.KEY!.value = "changed";
  doc.jobs[0]!.artifacts!.push("another");
  assert.deepEqual(j.toSpec().env, { KEY: { value: "original" } });
  assert.deepEqual(j.toSpec().artifacts, ["out/*"]);
});

test("group execution policy reaches every member without losing tool selectors", () => {
  const plan = new Plan();
  const source = plan.job("source", () => undefined);
  const a = plan.job("a", () => undefined),
    b = plan.job("b", () => undefined);
  const gen = source.generate(() => undefined);
  const group = plan.groupJobs("group", a, b);
  group
    .needs(source)
    .needsIfPresent("absent")
    .needsGenerated(gen)
    .timeout(0)
    .noProgressTimeout(0)
    .skipIf(() => false, { budgetMs: 0 })
    .beforeRun(() => {})
    .verify(() => {})
    .afterRun(() => {})
    .memo(() => "key", { ttlMs: 0 })
    .cache(npm())
    .artifacts("out/*")
    .consumes(source)
    .concurrency(new ConcurrencyGroup("cap"))
    .prefers("os=linux")
    .inline()
    .continueOnError()
    .env("CI", "true", startup());
  const [first, second] = plan.toDoc().jobs.slice(1);
  assert.ok(first && second);
  const { id: _firstID, closures: firstClosures, ...firstFields } = first;
  const { id: _secondID, closures: secondClosures, ...secondFields } = second;
  assert.deepEqual(firstFields, secondFields);
  assert.deepEqual(Object.keys(firstClosures!).sort(), Object.keys(secondClosures!).sort());
  assert.equal(first.inline, true);
  assert.equal(first.timeout_ms, 0);
  assert.equal(first.skip_if_budget_ms, 0);
});

test("sealed declarations reject predicate mutations while accepting fresh expansion code", async () => {
  const plan = new Plan();
  const job = plan.job("job");
  const step = job.step("step", () => undefined);
  job.skipIf(() => {
    job.optional();
    return false;
  });
  const generator = job.generate((g) => {
    g.job("added", () => undefined);
  });
  plan.toDoc();
  plan.seal();
  const mutations = [
    () => plan.job("new"),
    () => plan.priority(3),
    () => plan.checkout({ tags: true }),
    () => job.needs("later"),
    () => job.step("new", () => undefined),
    () => job.generate(() => {}),
    () => job.withArgs({ name: "new", type: "string" }),
    () => step.risk("prod"),
    () => step.result(),
  ];
  for (const mutate of mutations) assert.throws(mutate, (error) => error instanceof SDKError && error.code === "plan_drift");
  const predicate = plan.findClosure("job/skip_if/0");
  assert.ok(predicate?.kind === "skip_if");
  assert.throws(() => predicate.fn({} as Context), /declarations cannot change/);
  const generated = new Generated(job, generator.id, plan);
  generated.job("added", () => 1);
  plan.insert(generated);
  assert.throws(() => generated.job("later"), /declarations cannot change/);
  assert.equal(plan.toDoc().jobs.length, 2);
});

test("child targets compose parent and explicit repositories", () => {
  const plan = new Plan();
  plan.job("release", () => undefined).startsChildren("deploy").startsChildrenIn("gh:123", "publish").startsChildren("smoke");
  assert.deepEqual(plan.toDoc().jobs[0]!.children, [
    { pipeline: "deploy" }, { pipeline: "publish", repository: "gh:123" }, { pipeline: "smoke" },
  ]);
});


test("runner label declarations leave normalization to the host", () => {
  const plan = new Plan();
  plan.job("build", () => {}).requires("", " gpu ", "gpu", "gpu").prefers("", " fast ", "fast");
  const job = plan.toDoc().jobs[0];
  assert.deepEqual(job?.requires, ["", " gpu ", "gpu", "gpu"]);
  assert.deepEqual(job?.prefers, ["", " fast ", "fast"]);
});
