import { definePipeline, npm } from "@sparkwing/sdk";

definePipeline({
  name: "Release",
  short: "Test with a cached npm install, then deploy with a secret",
  // Declared secrets are checked before any job starts, so a missing one fails
  // the run up front with the command that sets it.
  secrets: [{ name: "DEPLOY_TOKEN" }],
  plan(plan) {
    const test = plan
      .job("test", async (ctx) => {
        await ctx.exec(["npm", "ci", "--no-audit", "--no-fund"]).run();
        await ctx.exec(["npm", "test"]).run();
      })
      .cache(npm())
      .timeout(10 * 60_000);

    plan
      .job("deploy", async (ctx) => {
        const token = await ctx.secret("DEPLOY_TOKEN");
        if (token === undefined) throw new Error("DEPLOY_TOKEN is not set");
        // The host masks the value in every log line, including this script's output.
        await ctx.exec(["./scripts/deploy.sh"]).env("DEPLOY_TOKEN", token).run();
      })
      .needs(test)
      .retry(2, { backoffMs: 10_000 })
      .timeout(5 * 60_000);
  },
});
