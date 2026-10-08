// A pipeline whose body leaves a referenced interval running, which on its own
// keeps a Node.js process alive forever.

import { definePipeline, main } from "../../src/index.ts";

definePipeline({
  name: "lingering",
  plan(plan) {
    plan.job("poll", () => {
      setInterval(() => undefined, 1000);
      return { started: true };
    });
  },
});

process.exitCode = await main();
