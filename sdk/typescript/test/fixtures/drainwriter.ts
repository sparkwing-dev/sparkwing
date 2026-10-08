// A pipeline whose body overruns stderr and queues more output from a drain
// handler, then leaves an interval running.

import { definePipeline, main } from "../../src/index.ts";

definePipeline({
  name: "drainwriter",
  plan(plan) {
    plan.job("flood", () => {
      process.stderr.once("drain", () => process.stderr.write("z".repeat(63 * 1024)));
      process.stderr.write("y".repeat(700 * 1024));
      setInterval(() => undefined, 1000);
    });
  },
});

process.exitCode = await main();
