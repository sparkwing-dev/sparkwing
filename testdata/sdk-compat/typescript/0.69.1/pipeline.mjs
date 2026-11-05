import { writeSync } from "node:fs";
import { definePipeline, main, dir, git, docker } from "@sparkwing/sdk";

definePipeline({
  name: "compat",
  short: "Frozen historical SDK compatibility fixture",
  args: [{ name: "name", type: "string", default: "guest" }],
  secrets: [{ name: "COMPAT_SECRET", optional: true }],
  plan(plan, run) {
    const source = plan.job("task").retry(2).timeout(60_000);
    source.step("output", () => ({ name: run.args.name })).result();
    for (const mode of ["exec", "fail", "cancel"]) {
      source.step(mode, async (ctx) => {
        await ctx.exec([process.env.COMPAT_HELPER_EXE, mode]).run();
      });
    }
    source.step("environment", async (ctx) => {
      process.env.COMPAT_MUTATED_ENV = "callback";
      await ctx.exec([process.env.COMPAT_HELPER_EXE, "env"]).run();
    });
    source.step("helper", async (ctx) => ({
      trimmed: await ctx.exec([process.env.COMPAT_HELPER_EXE, "trim"]).string(),
    }));
    source.step("raw", async (ctx) => {
      writeSync(1, "compat-raw-out\n");
      await ctx.exec([process.env.COMPAT_HELPER_EXE, "raw"]).run();
    });
    source.step("oidc", async (ctx) => ({ token: await ctx.oidcToken("compat-audience") }));
    source.step("secret", async (ctx) => ({ secret: await ctx.secret("COMPAT_SECRET") }));
    source.step("git", (ctx) => git.changedFiles(ctx, process.cwd(), ""));
    source.step("docker", (ctx) => docker.build(ctx, { image: "compat-image", tags: ["fixture"] }));
    source.memo(() => "compat-memo");
    source.generate((generated) => {
      generated.job("generated", () => null);
    });
    const lifecycle = plan.job("lifecycle").retry(2).timeout(60_000).cache(dir("compat-cache", "compat-cache-key.txt"));
    const concurrent = async (ctx) => { await ctx.exec([process.env.COMPAT_HELPER_EXE, "concurrent"]).run(); };
    const a = lifecycle.step("concurrent-a", concurrent);
    const b = lifecycle.step("concurrent-b", concurrent);
    lifecycle.step("retry", async (ctx) => { await ctx.exec([process.env.COMPAT_HELPER_EXE, "retry"]).run(); }).needs(a, b);
    plan.job("dependent", () => null).needs(source);
  },
});
await main();
