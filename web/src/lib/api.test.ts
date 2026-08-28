import assert from "node:assert/strict";
import { before, describe, it } from "node:test";

type RuntimeWindow = { __SPARKWING_API_URL__?: string };
let getNodeStreamUrl!: typeof import("./api").getNodeStreamUrl;

before(async () => {
  const runtime = globalThis as unknown as { window?: RuntimeWindow };
  const hadWindow = Object.prototype.hasOwnProperty.call(runtime, "window");
  const previousWindow = runtime.window;
  runtime.window = { __SPARKWING_API_URL__: "" };
  try {
    ({ getNodeStreamUrl } = await import("./api"));
  } finally {
    if (hadWindow) runtime.window = previousWindow;
    else delete runtime.window;
  }
});

describe("getNodeStreamUrl", () => {
  it("requests structured events for the live log parser", () => {
    const url = getNodeStreamUrl("run-one", "verify");
    assert.equal(
      url,
      "/api/v1/runs/run-one/logs/verify/stream?format=ndjson",
    );
    assert.doesNotMatch(url, /format=ansi/);
  });
});
