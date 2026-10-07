// A two-job pipeline: build publishes a digest, publish reads it through the
// engine's output route. Run `node pipeline.ts --describe` to see what the
// engine reads first.

import { definePipeline, main } from "../../src/index.ts";

definePipeline({
  name: "hello",
  short: "Build, then publish what the build produced",
  args: [{ name: "target", desc: "where to publish", default: "staging" }],
  plan(plan, run) {
    const build = plan.job("build", (ctx) => {
      ctx.log.info("building");
      return { digest: "sha-hello" };
    });
    plan
      .job("publish", async (ctx) => {
        const out = await ctx.output<{ digest: string }>("build");
        if (!out) throw new Error("build recorded no output");
        ctx.log.info(`published ${out.digest} to ${run.args["target"] ?? "staging"}`);
      })
      .needs(build)
      .retry(2, { backoffMs: 1000 })
      .timeout(60_000);
  },
});

process.exitCode = await main();
