import assert from "node:assert/strict";
import { test } from "node:test";
import { Plan, resolveArgs } from "../src/plan.ts";

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
  assert.throws(() => j.retry(-1), /non-negative integer/);
  assert.throws(() => j.retry(1, { backoffMs: 1.5 }), /backoff/);
  assert.throws(() => j.timeout(0), /positive integer/);
  assert.throws(() => j.timeout(2.5), /positive integer/);
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
