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

test("registration rejects duplicate pipelines", () => {
  definePipeline({ name: "p", plan() {} });
  assert.throws(() => definePipeline({ name: "p", plan() {} }), /already defined/);
});

test("describe forwards shared argument names for host validation", async () => {
  definePipeline({
    name: "collision",
    args: [{ name: "arg", type: "string" }],
    plan(p) { p.job("build", () => undefined).withArgs({ name: "arg", type: "string" }); },
  });
  const doc = await describe(run);
  assert.equal(doc.pipelines[0]!.error, undefined);
  assert.equal(doc.pipelines[0]!.args.length, 2);
});

test("standalone describe returns author errors for host sanitization", async () => {
  const secret = "private-from-declaration";
  definePipeline({
    name: "broken",
    args: [{ name: "token", type: "string", secret: true, default: secret }],
    plan() { throw new Error(secret); },
  });
  const error = (await describe(run)).pipelines[0]!.error!;
  assert.equal(error.code, "plan_failed");
  assert.match(error.message, /describe pipeline "broken"/);
  assert.match(error.message, /private-from-declaration/);
});
