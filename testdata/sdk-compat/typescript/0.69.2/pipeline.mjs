import { writeSync } from "node:fs";
import { definePipeline, main } from "@sparkwing/sdk";

definePipeline({
  name: "compat",
  short: "Frozen historical SDK compatibility fixture",
  args: [{ name: "name", type: "string", default: "guest" }],
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
    source.memo(() => "compat-memo");
    source.generate((generated) => {
      generated.job("generated", () => null);
    });
    plan.job("dependent", () => null).needs(source);
  },
});
await main();
