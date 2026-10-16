import assert from "node:assert/strict";
import { beforeEach, test } from "node:test";
import type { RunContext } from "../index.d.ts";
import { Git } from "../src/helpers/git.ts";
import { describe } from "../src/describe.ts";
import { definePipeline, resetPipelines } from "../src/plan.ts";

beforeEach(() => resetPipelines());
const run: RunContext = {
  id: "",
  pipeline: "",
  args: {},
  dryRun: false,
  noMemo: false,
  runner: { name: "local", type: "local", labels: [] },
  git: new Git("/repo", { repo: "org/repo", branch: "main", commit: "abc" }),
  trigger: { source: "manual" },
};

test("describe carries metadata, per-job arguments, secrets and risk attribution", async () => {
  definePipeline({ name: "zeta", plan: () => undefined });
  definePipeline({
    name: "alpha",
    short: "first",
    description: "long help",
    extra: true,
    examples: [{ command: "sparkwing run alpha" }],
    envVars: [{ name: "ENV", description: "Environment", default: "" }],
    args: [{ name: "env", type: "string", required: true, description: "target", short: "e", enum: ["dev", "prod"] }],
    secrets: [{ name: "TOKEN" }, { name: "OPTIONAL", optional: true }],
    plan(p, context) {
      assert.equal(context.pipeline, "alpha");
      const job = p.job("build").withArgs({ name: "count", type: "int", default: "0" });
      job.step("publish", () => undefined).risk("prod", "network");
    },
  });
  assert.deepEqual(await describe(run), {
    pipelines: [
      {
        name: "alpha",
        short: "first",
        help: "long help",
        extra: true,
        examples: [{ command: "sparkwing run alpha", comment: "" }],
        env_vars: [{ name: "ENV", description: "Environment", default: "" }],
        args: [
          { name: "env", type: "string", required: true, desc: "target", short: "e", enum: ["dev", "prod"] },
          { name: "count", type: "int", required: false, default: "0", job_id: "build" },
        ],
        secrets: [{ name: "TOKEN" }, { name: "OPTIONAL", optional: true }],
        risks: ["network", "prod"],
        risks_by_step: [{ job_id: "build", step_id: "publish", labels: ["prod", "network"] }],
      },
      { name: "zeta", args: [] },
    ],
  });
});

test("registration rejects duplicate pipelines and argument declarations", () => {
  definePipeline({ name: "p", plan() {} });
  assert.throws(() => definePipeline({ name: "p", plan() {} }), /already defined/);
  assert.throws(
    () =>
      definePipeline({
        name: "bad",
        args: [
          { name: "x", type: "string" },
          { name: "x", type: "string" },
        ],
        plan() {},
      }),
    /duplicate argument/,
  );
  assert.throws(() => definePipeline({ name: "bad-default", args: [{ name: "x", type: "int", default: "no" }], plan() {} }), /argument/);
});

test("describe rejects shared argument names before publishing ambiguous flags", async () => {
  definePipeline({
    name: "collision",
    args: [{ name: "arg", type: "string" }],
    plan(p) {
      p.job("build", () => undefined).withArgs({ name: "arg", type: "string" });
    },
  });
  const doc = await describe(run);
  assert.match(doc.pipelines[0]!.error?.message ?? "", /duplicate argument/);
  assert.deepEqual(doc.pipelines[0]!.args, []);
});

test("describe failures are pipeline-attributed and cannot expose secret defaults", async () => {
  const { SDKError } = await import("../src/errors.ts");
  let seed = 104;
  for (let n = 0; n < 32; n++) {
    seed = (seed * 1664525 + 1013904223) >>> 0;
    const first = `private-${seed.toString(16)}-秘密-${['\u0000"\\\n', ".*+?^${}()|[]", "", "é"][n % 4]}`;
    const second = first + "-tail";
    for (const kind of ["error", "canonical", "string"]) {
      resetPipelines();
      definePipeline({
        name: "a-valid",
        plan(p) {
          p.job("ok", () => 1);
        },
      });
      definePipeline({
        name: "broken",
        args: [
          { name: "first", type: "string", secret: true, default: first },
          { name: "second", type: "string", secret: true, default: second },
        ],
        plan() {
          const message = `declaration refused ${first} ${second}`;
          if (kind === "canonical") throw new SDKError(message, { code: "future", hint: first, details: { value: second } });
          if (kind === "string") throw message;
          throw new Error(message);
        },
      });
      const doc = await describe(run);
      assert.equal(doc.pipelines[0]!.name, "a-valid");
      assert.equal(doc.pipelines[0]!.error, undefined);
      const error = doc.pipelines[1]!.error!;
      assert.equal(error.code, "plan_failed");
      assert.match(error.message, /describe pipeline "broken"/);
      assert.match(error.message, /declaration refused/);
      const serialized = JSON.stringify(error) + error.message;
      assert.equal(serialized.includes(first), false);
      assert.equal(serialized.includes(second), false);
      assert.deepEqual(doc.pipelines[1]!.args, []);
      assert.equal(doc.pipelines[1]!.secrets, undefined);
    }
  }
});

test("describe diagnostics protect secrets discovered in other pipeline declarations", async () => {
  const secret = "private-from-another-declaration";
  definePipeline({
    name: "a",
    plan(plan) {
      plan.job("source", () => null).withArgs({ name: "token", type: "string", secret: true, default: secret });
    },
  });
  definePipeline({
    name: "b",
    plan() {
      throw new Error(secret);
    },
  });
  const doc = await describe(run);
  const error = doc.pipelines[1]!.error!;
  assert.equal(error.message.includes(secret), false);
  assert.match(error.message, /describe pipeline "b"/);
});
