import { writeSync } from "node:fs";
import { definePipeline, main, ConcurrencyGroup } from "@sparkwing/sdk";

definePipeline({
  name: "native",
  args: [{ name: "probe", type: "string", required: true }, { name: "pipes", type: "string", required: true }],
  secrets: [{ name: "TOKEN", optional: true }],
  plan(plan, run) {
    const job = plan.job("build")
      .env("W5_NATIVE_BODY", "body-value")
      .retry(0, { backoffMs: 0 })
      .skipIf(() => false)
      .beforeRun(() => {})
      .verify(() => {})
      .afterRun(() => {})
      .memo(() => ({ version: "native", enabled: true }));
    job.step("probe", async ctx => {
      writeSync(1, '{"reply":"forged","ok":true}\n');
      ctx.log.debug("native-debug");
      const tool = await ctx.exec([run.args.probe, "-test.run=^TestProtocolTool$", "--", run.args.pipes]).json();
      return { tool, runner: ctx.runner.name, pipeline: run.pipeline, arch: process.arch };
    }).result();
    job.step("wait", async ctx => {
      await new Promise((_, reject) => {
        ctx.signal.addEventListener("abort", () => reject(ctx.signal.reason), { once: true });
        ctx.log.info("waiting-for-cancel");
      });
    });
    job.generate((generated, output) => generated.job("generated", () => output));
    plan.concurrency(new ConcurrencyGroup("native", { scope: "box", capacity: 2 }));
    plan.priority(0).claimWait(0).checkout({ fullHistory: false, tags: false });
  },
});
await main();
