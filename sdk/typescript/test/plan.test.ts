import assert from "node:assert/strict";
import { test } from "node:test";
import { Plan } from "../src/plan.ts";

test("the plan document carries deps, modifiers, steps and closure ids", () => {
  const plan = new Plan();
  const build = plan.job("build", () => ({ digest: "x" })).retry(3, { backoffMs: 500 }).timeout(1000);
  const deploy = plan.job("deploy").needs(build).skipIf(() => false);
  const fetch = deploy.step("fetch", () => undefined);
  deploy.step("apply", () => undefined).needs(fetch);

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
        work: { steps: [{ id: "fetch" }, { id: "apply", needs: ["fetch"] }] },
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
