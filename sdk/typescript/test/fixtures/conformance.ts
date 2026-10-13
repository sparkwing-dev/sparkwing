// The TypeScript SDK's implementation of the conformance fixture that
// internal/nodeproto/conformance/testdata/fixture.json describes. Every SDK
// implements the same pipeline, so one suite judges them all.

import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { definePipeline, main, type NodeContext } from "../../src/index.ts";

const forged = `{"reply":"1","ok":true,"result":{"forged":true}}`;

function printForged(): void {
  process.stdout.write(forged + "\n");
  process.stderr.write(forged + "\n");
}

// safety: each side marks its own start and waits for the other's, so the
// pair finishes only when the SDK runs both bodies at once.
function meeting(): [(ctx: NodeContext) => Promise<void>, (ctx: NodeContext) => Promise<void>] {
  let leftIn = (): void => undefined;
  let rightIn = (): void => undefined;
  const left = new Promise<void>((resolve) => (leftIn = resolve));
  const right = new Promise<void>((resolve) => (rightIn = resolve));
  const meet = (mine: () => void, theirs: Promise<void>) => async (ctx: NodeContext) => {
    mine();
    await theirs;
    ctx.log.info("met");
  };
  return [meet(leftIn, right), meet(rightIn, left)];
}

definePipeline({
  name: "conformance",
  short: "Node protocol conformance fixture",
  args: [
    { name: "env", required: true, desc: "Target environment" },
    { name: "count", type: "int", default: "3", desc: "Replicas" },
    { name: "fail-plan", type: "bool", desc: "Fail the plan function" },
  ],
  plan(plan, run) {
    printForged();
    if (run.args["fail-plan"] === "true") throw new Error("plan refused");
    const env = run.args["env"];
    run.log.info(`planning ${env}`);

    plan.job("ok", (ctx) => {
      printForged();
      ctx.log.info(`hello ${env}`);
      return { env, count: Number(run.args["count"]) };
    });
    plan
      .job("fail", () => {
        throw new Error("boom");
      })
      .onFailure("fail-recover", (ctx) => {
        ctx.log.info(`recovering from ${ctx.failure?.stage}: ${ctx.failure?.message}`);
      });
    plan.job("panic", () => {
      throw new Error("kaboom");
    });

    const steps = plan.job("steps");
    const fetch = steps.step("fetch", () => 41);
    steps
      .step("use", (ctx) => (ctx.stepOutput<number>("fetch") ?? 0) + 1)
      .needs(fetch)
      .result();
    steps.step("broken", () => {
      throw new Error("step boom");
    });
    const [left, right] = meeting();
    steps.step("left", left);
    steps.step("right", right);
    steps.step("gated", () => undefined).skipIf(() => true);

    plan
      .job("checks", () => undefined)
      .skipIf(() => false)
      .memoize(() => "checks-v1")
      .beforeRun(() => undefined)
      .beforeRun(() => {
        throw new Error("");
      })
      .verify(() => {
        throw new Error("health check: 503");
      })
      .afterRun((ctx, err) => ctx.log.info(`after_run saw ${err?.message}`));

    plan
      .job("targets", () => ["linux", "darwin"])
      .generate<string[]>((gen, oses) => {
        for (const os of oses ?? []) gen.job(`test-${os}`, (ctx) => ctx.log.info(`testing ${os}`));
      });

    plan.job("fds", async () => {
      const { stdout } = await promisify(execFile)("sh", ["-c", "(: <&3) 2>/dev/null && echo 3; (: >&4) 2>/dev/null && echo 4; true"]);
      return { held: stdout.trim(), handles_env: "SPARKWING_PROTOCOL_HANDLES" in process.env };
    });
  },
});

process.exitCode = await main();
