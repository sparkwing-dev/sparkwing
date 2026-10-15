import { definePipeline, main } from "@sparkwing/sdk";

definePipeline({
  name: "conformance",
  short: "Node protocol v2 conformance",
  args: [{ name: "name", type: "string", required: true }],
  plan(plan, run) {
    if (run.args.name === "refuse") throw new Error("fixture plan refused");
    const job = plan.job("task").failurePolicy("fail_fast");
    job.step("run", (ctx) => {
      ctx.log.info(`running ${run.args.name}`);
      if (run.args.name === "fail-body") throw new Error("fixture body failed");
      return null;
    });
    job.beforeRun(() => {
      if (run.args.name === "fail-eval") throw new Error("fixture hook failed");
    });
    job.verify(() => {});
    job.afterRun(() => {});
    job.skipIf(() => false);
    job.memo(() => "fixture-key");
    job.generate((generated) => {
      generated
        .job("generated")
        .failurePolicy("fail_fast")
        .step("run", () => null);
    });
  },
});
await main();
