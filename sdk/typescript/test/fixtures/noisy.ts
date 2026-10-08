// A pipeline whose body writes a burst of stderr larger than a pipe buffer
// just before the node finishes.

import { definePipeline, main } from "../../src/index.ts";

definePipeline({
  name: "noisy",
  plan(plan) {
    plan.job("shout", () => {
      const line = "x".repeat(1024);
      for (let i = 0; i < 4096; i++) console.error(`${i} ${line}`);
    });
  },
});

process.exitCode = await main();
