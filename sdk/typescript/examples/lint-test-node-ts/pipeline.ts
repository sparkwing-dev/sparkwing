import { definePipeline, npm } from "@sparkwing/sdk";

// One job with three steps: install once, then lint and test side by side.
// Steps share the job's workspace, so both read the same node_modules.
definePipeline({
  name: "Check",
  short: "Install, then lint and test",
  plan(plan) {
    const check = plan.job("check").cache(npm()).timeout(15 * 60_000);
    const install = check.step("install", async (ctx) => {
      await ctx.exec(["npm", "ci", "--no-audit", "--no-fund"]).run();
    });
    check.step("lint", async (ctx) => {
      await ctx.exec(["npm", "run", "lint"]).run();
    }).needs(install);
    check.step("test", async (ctx) => {
      await ctx.exec(["npm", "test"]).run();
    }).needs(install);
  },
});
