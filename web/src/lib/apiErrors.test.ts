import assert from "node:assert/strict";
import { before, describe, it } from "node:test";

let api!: typeof import("./api");
before(async () => { api = await import("./api"); });

describe("console server failures", () => {
  it("hides raw bodies in list, approval and schedule errors", async () => {
    const previousFetch = globalThis.fetch;
    try {
      for (const body of ['{"error":{"message":"private database failure"}}', "<html>proxy failure</html>"]) {
        globalThis.fetch = async () => new Response(body, {
          status: 500, headers: { "X-Request-Id": "req-console-500" },
        });
        for (const call of [
          () => api.listRuns(new URLSearchParams()),
          () => api.getRun("run"),
          () => api.resolveApproval("run", "job", "approved", ""),
          () => api.pauseCron("schedule"),
        ]) {
          await assert.rejects(call(), (error: Error) => {
            assert.match(error.message, /Try again/);
            assert.match(error.message, /support@sparkwing.dev/);
            assert.match(error.message, /Request ID: req-console-500/);
            assert.doesNotMatch(error.message, /private|database|html|\{|\}/);
            return true;
          });
        }
      }
    } finally {
      globalThis.fetch = previousFetch;
    }
  });

  it("uses the deployment support contact and omits an absent request id", async () => {
    const previousFetch = globalThis.fetch;
    const runtime = globalThis as unknown as { window?: { __SPARKWING_SUPPORT_CONTACT__?: string } };
    const previousWindow = runtime.window;
    try {
      runtime.window = { __SPARKWING_SUPPORT_CONTACT__: "help@example.com" };
      globalThis.fetch = async () => new Response("unavailable", { status: 502 });
      await assert.rejects(api.listRuns(new URLSearchParams()), (error: Error) => {
        assert.match(error.message, /contact help@example.com/);
        assert.doesNotMatch(error.message, /Request ID|support@sparkwing.dev|unavailable/);
        assert.equal(api.getConnectionStatus(), "ok");
        return true;
      });
    } finally {
      globalThis.fetch = previousFetch;
      if (previousWindow === undefined) delete runtime.window;
      else runtime.window = previousWindow;
    }
  });
});
