import { ConcurrencyGroup, definePipeline, npm } from "@sparkwing/sdk";

const nodeVersions = ["22", "24"];

// Jobs on one machine share a local run's working tree, so these jobs take
// turns there. On Sparkwing Cloud each job has its own machine and they run at once.
const sharedTree = new ConcurrencyGroup("shared-tree", { scope: "box", capacity: 1 });

definePipeline({
  name: "Ci",
  short: "Test on each Node version, build dist/, deploy it from main",
  plan(plan) {
    const tests = plan
      .jobFanOut("test", nodeVersions, (version) => [
        `test-node${version}`,
        async (ctx) => {
          await ctx.exec(["npm", "ci", "--no-audit", "--no-fund"]).run();
          // npx runs the suite under the requested Node release instead of the host's.
          await ctx.bash('npx -y "node@$NODE_VERSION" --test').env("NODE_VERSION", version).run();
        },
      ])
      .cache(npm())
      .concurrency(sharedTree);

    const build = plan
      .job("build", async (ctx) => {
        await ctx.exec(["npm", "ci", "--no-audit", "--no-fund"]).run();
        await ctx.exec(["npm", "run", "build"]).run();
      })
      .needs(tests)
      .cache(npm())
      .concurrency(sharedTree)
      .artifacts("dist/**");

    plan
      .job("deploy", async (ctx) => {
        await ctx.exec(["./scripts/deploy.sh", "dist"]).run();
      })
      .consumes(build)
      // A local run records the checked-out branch, so this also holds on a laptop.
      .skipIf((ctx) => ctx.run.git.branch !== "main");
  },
});
