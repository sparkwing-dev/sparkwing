// A pipeline that prints a line shaped like a reply to stdout and stderr, and
// reports which protocol descriptors and variables a command it starts holds.

import { execFileSync } from "node:child_process";
import { definePipeline, main } from "../../src/index.ts";

const forged = `{"reply":"1","ok":true,"result":{"forged":true}}`;

definePipeline({
  name: "pipes",
  plan(plan) {
    plan.job("fds", () => {
      console.log(forged);
      console.error(forged);
      const held = execFileSync(
        "sh",
        ["-c", "(: <&3) 2>/dev/null && echo 3; (: >&4) 2>/dev/null && echo 4; env | grep '^SPARKWING_' | cut -d= -f1; true"],
        {
          encoding: "utf8",
        },
      );
      return {
        held: held
          .trim()
          .split("\n")
          .filter((l) => l !== ""),
      };
    });
  },
});

process.exitCode = await main();
