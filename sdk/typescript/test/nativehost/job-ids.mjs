import { definePipeline, main } from "@sparkwing/sdk";

definePipeline({
  name: "job-ids",
  args: [{ name: "id", type: "string", required: true }],
  plan(plan, run) {
    plan.job(run.args.id).step("run", () => null);
  },
});
await main();
