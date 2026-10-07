import assert from "node:assert/strict";
import { beforeEach, test } from "node:test";
import { describe } from "../src/describe.ts";
import { definePipeline, resetPipelines } from "../src/plan.ts";

beforeEach(() => resetPipelines());

test("describe names the protocol and every pipeline with its args", () => {
  definePipeline({ name: "zeta", plan: () => undefined });
  definePipeline({
    name: "alpha",
    short: "first",
    args: [
      { name: "env", required: true, desc: "target", enum: ["dev", "prod"] },
      { name: "token", secret: true },
    ],
    plan: () => undefined,
  });
  assert.deepEqual(describe(), {
    protocol: "1",
    pipelines: [
      {
        name: "alpha",
        short: "first",
        args: [
          { name: "env", go_name: "env", type: "string", required: true, desc: "target", enum: ["dev", "prod"] },
          { name: "token", go_name: "token", type: "string", required: false, secret: true },
        ],
      },
      { name: "zeta", args: [] },
    ],
  });
});

test("a pipeline name is defined once", () => {
  definePipeline({ name: "p", plan: () => undefined });
  assert.throws(() => definePipeline({ name: "p", plan: () => undefined }), /already defined/);
});
