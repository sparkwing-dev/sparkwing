import assert from "node:assert/strict";
import { beforeEach, test } from "node:test";
import { describe, PROTOCOL_VERSION } from "../src/describe.ts";
import { definePipeline, resetPipelines } from "../src/plan.ts";

beforeEach(() => resetPipelines());

test("describe names the protocol, the SDK and every pipeline with its args", () => {
  definePipeline({ name: "zeta", plan: () => undefined });
  definePipeline({
    name: "alpha",
    short: "first",
    args: [{ name: "env", required: true, desc: "target" }, { name: "token", secret: true }],
    plan: () => undefined,
  });
  assert.deepEqual(describe(), {
    protocol: PROTOCOL_VERSION,
    sdk: { language: "typescript", version: "0.0.0" },
    pipelines: [
      {
        name: "alpha",
        short: "first",
        args: [
          { name: "env", type: "string", required: true, desc: "target" },
          { name: "token", type: "string", required: false, secret: true },
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
