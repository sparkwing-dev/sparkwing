import assert from "node:assert/strict";
import { test } from "node:test";
import { ConcurrencyGroup, dirCache, goModulesCache, Plan, resolveArgs } from "../src/plan.ts";

test("the plan document carries deps, modifiers, steps and closure ids", () => {
  const plan = new Plan();
  const build = plan
    .job("build", () => ({ digest: "x" }))
    .retry(3, { backoffMs: 500 })
    .timeout(1000);
  const deploy = plan
    .job("deploy")
    .needs(build)
    .skipIf(() => false);
  const fetch = deploy.step("fetch", () => undefined);
  deploy
    .step("apply", () => undefined)
    .needs(fetch)
    .result();

  assert.deepEqual(plan.toDoc("ship", "run-1"), {
    protocol: "1",
    pipeline: "ship",
    run_id: "run-1",
    nodes: [
      { id: "build", deps: [], modifiers: { retry: 3, retry_backoff_ms: 500, timeout_ms: 1000 } },
      {
        id: "deploy",
        deps: ["build"],
        modifiers: { has_skip_if: true },
        work: { steps: [{ id: "fetch" }, { id: "apply", needs: ["fetch"], is_result: true }], result_step: "apply" },
        closures: { skip_if: ["deploy/skip_if/0"] },
      },
    ],
  });
});

test("a need on a job that does not exist is refused", () => {
  const plan = new Plan();
  plan.job("a", () => undefined).needs("ghost");
  assert.throws(() => plan.toDoc("p", "r"), /a needs ghost, which does not exist/);
});

test("a cycle is refused with its path", () => {
  const plan = new Plan();
  plan.job("a", () => undefined).needs("b");
  plan.job("b", () => undefined).needs("a");
  assert.throws(() => plan.toDoc("p", "r"), /cycle a -> b -> a/);
});

test("a step cycle inside a job is refused", () => {
  const plan = new Plan();
  const j = plan.job("j");
  j.step("x", () => undefined).needs("y");
  j.step("y", () => undefined).needs("x");
  assert.throws(() => plan.toDoc("p", "r"), /job j steps: cycle/);
});

test("duplicate ids, bad ids, empty jobs and mixed body and steps are refused", () => {
  const plan = new Plan();
  plan.job("a", () => undefined);
  assert.throws(() => plan.job("a"), /duplicate job a/);
  assert.throws(() => plan.job("has space"), /must match/);
  assert.throws(() => plan.lookup("a")?.step("s", () => undefined), /cannot also have steps/);
  plan.job("empty");
  assert.throws(() => plan.toDoc("p", "r"), /empty has neither a body nor steps/);
});

test("retry and timeout reject values the schema cannot carry", () => {
  const plan = new Plan();
  const j = plan.job("a", () => undefined);
  assert.throws(() => j.retry(-1), /retry attempts must be an integer of at least 0/);
  assert.throws(() => j.retry(1, { backoffMs: 1.5 }), /backoff/);
  assert.throws(() => j.timeout(0), /timeout in milliseconds must be an integer of at least 1/);
  assert.throws(() => j.timeout(2.5), /at least 1/);
});

test("a job has at most one result step", () => {
  const plan = new Plan();
  const j = plan.job("j");
  j.step("a", () => undefined).result();
  j.step("b", () => undefined).result();
  assert.throws(() => plan.toDoc("p", "r"), /more than one result step/);
});

test("a wait on a generator that does not exist is refused", () => {
  const plan = new Plan();
  const other = new Plan().job("elsewhere", () => undefined).generate(() => undefined);
  plan.job("a", () => undefined).needsGenerated(other);
  assert.throws(() => plan.toDoc("p", "r"), /waits on generator elsewhere\/generator\/0, which does not exist/);
});

test("generated jobs are checked against the plan before they join it", () => {
  const plan = new Plan();
  plan.job("src", () => undefined);
  plan.toDoc("p", "r");
  const clash = new Plan().job("src", () => undefined);
  assert.throws(() => plan.insert([clash]), /collides/);
  const dangling = new Plan().job("x", () => undefined).needs("ghost");
  assert.throws(() => plan.insert([dangling]), /x needs ghost/);
  assert.equal(plan.jobs.length, 1, "a refused insert adds nothing");
});

test("args are checked against the declared flags and take their defaults", () => {
  const def = {
    name: "p",
    args: [
      { name: "env", required: true, enum: ["dev", "prod"] },
      { name: "count", type: "int" as const, default: "3" },
      { name: "fast", type: "bool" as const },
    ],
    plan: () => undefined,
  };
  assert.deepEqual(resolveArgs(def, { env: "prod" }), { env: "prod", count: "3" });
  assert.deepEqual(resolveArgs(def, { env: "dev", count: "-2", fast: "true" }), { env: "dev", count: "-2", fast: "true" });
  assert.match(String(resolveArgs(def, {})), /--env is required/);
  assert.match(String(resolveArgs(def, { env: "qa" })), /not one of dev, prod/);
  assert.match(String(resolveArgs(def, { env: "dev", count: "1.5" })), /not an integer/);
  assert.match(String(resolveArgs(def, { env: "dev", fast: "yes" })), /not true or false/);
  assert.match(String(resolveArgs(def, { env: "dev", ghost: "1" })), /no flag --ghost/);
});

test("the plan document carries caches, memo, approvals, recovery, concurrency, resources and step flags", () => {
  const plan = new Plan();
  const deploys = new ConcurrencyGroup("deploys", { capacity: 2, scope: "run", onLimit: "queue", queueTimeoutMs: 5000 });
  plan.concurrency(deploys).concurrency(new ConcurrencyGroup("fleet")).resources({ cores: 2 });
  const gate = plan.approval("gate", { message: "ship?", timeoutMs: 60_000, onTimeout: "deny" });
  const build = plan
    .job("build", () => undefined)
    .needs(gate)
    .cacheDir(goModulesCache(), dirCache("./web/node_modules/", "web/package-lock.json"))
    .memoize(() => "k", { ttlMs: 100 * 86_400_000 })
    .concurrency(deploys, 2)
    .resources({ cores: 1.5, memoryGB: 0.5 })
    .requires("linux", "")
    .retry(2, { auto: true })
    .env("CI", "1")
    .onFailure("build-recover", () => undefined);
  const checks = plan.job("test").needs(build).failurePolicy("collect-all");
  checks
    .step("unit", () => undefined)
    .skipIf(() => false)
    .continueOnError();
  checks
    .step("cleanup", () => undefined)
    .finally()
    .safeWithoutDryRun();

  const doc = plan.toDoc("ship", "run-1");
  assert.deepEqual(doc.plan_concurrency_groups, [
    { key: "r:5:run-1deploys", cost: 1, capacity: 2, on_limit: "queue", queue_timeout_ms: 5000 },
    { key: "g:fleet", cost: 1 },
  ]);
  assert.deepEqual(doc.plan_concurrency, doc.plan_concurrency_groups?.[0]);
  assert.deepEqual(doc.plan_resources, { cores: 2 });
  assert.deepEqual(
    doc.nodes.map((n) => n.id),
    ["gate", "build", "test", "build-recover"],
  );
  assert.deepEqual(doc.nodes[0], { id: "gate", deps: [], approval: { message: "ship?", timeout_ms: 60_000, on_timeout: "deny" } });
  assert.deepEqual(doc.nodes[1], {
    id: "build",
    deps: ["gate"],
    env: { CI: "1" },
    modifiers: {
      cache: true,
      cache_ttl_ms: 35 * 86_400_000,
      conc_group: "deploys",
      conc_capacity: 2,
      conc_cost: 2,
      conc_scope: "run",
      conc_on_limit: "queue",
      conc_queue_timeout_ms: 5000,
      res_cores: 1.5,
      res_memory_bytes: 2 ** 29,
      runs_on: ["linux"],
      retry: 2,
      retry_auto: true,
      on_failure: "build-recover",
    },
    caches: [
      { name: "go-modules", resolver: "gomod", key_files: ["go.sum"] },
      { name: "node_modules", path: "./web/node_modules/", key_scope: "web/node_modules", key_files: ["web/package-lock.json"] },
    ],
    memo_key: { closure: "build/cache_key/0" },
    closures: { cache_key: "build/cache_key/0" },
  });
  assert.deepEqual(doc.nodes[2]?.work, {
    steps: [
      { id: "unit", has_skip_if: true, continue_on_error: true, closures: { skip_if: ["test/step/unit/skip_if/0"] } },
      { id: "cleanup", finally: true, safe_without_dry_run: true },
    ],
    failure_policy: "collect-all",
  });
  assert.deepEqual(doc.nodes[3], { id: "build-recover", deps: [], on_failure_of: "build" });
});

test("costs over capacity, plan-level box scope, bad resources and colliding recovery ids are refused", () => {
  const plan = new Plan();
  const j = plan.job("a", () => undefined);
  assert.throws(() => j.concurrency(new ConcurrencyGroup("g"), 2), /exceeds group g capacity 1/);
  assert.throws(() => plan.concurrency(new ConcurrencyGroup("g", { scope: "box" })), /box scope/);
  assert.throws(() => j.resources({ cores: 0 }), /positive, finite/);
  assert.throws(() => j.cacheDir(dirCache("d")), /at least one key file/);
  plan.job("b", () => undefined);
  j.onFailure("b", () => undefined);
  assert.throws(() => plan.toDoc("p", "r"), /duplicate job b/);
});
