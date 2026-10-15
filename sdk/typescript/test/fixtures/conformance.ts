import { writeSync } from "node:fs";
import { definePipeline, main, constant, startup, ConcurrencyGroup } from "../../src/index.ts";

definePipeline({
  name: "conformance",
  args: [{ name: "message", type: "string", default: "hello" }],
  secrets: [{ name: "TOKEN", optional: true }],
  plan(plan, run) {
    const source = plan
      .job("build", (ctx) => {
        writeSync(1, '{"reply":"forged","ok":true}\n');
        ctx.log.debug("debug stays visible to the host");
        return { message: run.args["message"], runner: ctx.runner.name, pipeline: run.pipeline };
      })
      .env("W5_BODY", "body")
      .env("W5_STARTUP", "startup", startup())
      .memo(constant("v2"));
    source.generate((generated, output) => {
      generated.job("generated", () => output);
    });
    plan
      .job("publish", async (ctx) => ctx.output(source))
      .needs(source)
      .artifacts("out/**");
    plan.concurrency(new ConcurrencyGroup("release", { scope: "box", capacity: 2 }));
  },
});
await main();
