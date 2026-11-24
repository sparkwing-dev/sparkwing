import assert from "node:assert/strict";
import { describe, it, type TestContext } from "node:test";
import { authFetch, getConnectionStatus, getRuns } from "./api";

function hangUntilAborted(seen: { signal?: AbortSignal | null }) {
  return (_input: unknown, init?: RequestInit) => {
    seen.signal = init?.signal;
    return new Promise<Response>((_resolve, reject) => {
      init?.signal?.addEventListener("abort", () =>
        reject(new DOMException("signal is aborted without reason", "AbortError")),
      );
    });
  };
}

function withFetch(t: TestContext, fetch: typeof globalThis.fetch) {
  const previous = globalThis.fetch;
  globalThis.fetch = fetch;
  t.after(() => {
    globalThis.fetch = previous;
  });
}

describe("request deadline", () => {
  it("rejects a read past its deadline with a sentence naming the slow controller", async (t) => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    withFetch(t, hangUntilAborted({}) as typeof globalThis.fetch);
    const pending = authFetch("/api/v1/runs");
    t.mock.timers.tick(10_000);
    await assert.rejects(pending, (err: Error) => {
      assert.equal(err.name, "RequestTimeoutError");
      assert.equal(
        err.message,
        "The Sparkwing controller did not answer within 10 seconds. It may be busy.",
      );
      return true;
    });
    assert.equal(getConnectionStatus(), "ok", "a slow controller was reached, so the banner must not call it down");
  });

  it("lets a slow overview read outlast the ordinary deadline and still answer", async (t) => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    const seen: { signal?: AbortSignal | null } = {};
    let answer!: (response: Response) => void;
    withFetch(t, (async (_input: unknown, init?: RequestInit) => {
      seen.signal = init?.signal;
      return new Promise<Response>((resolve) => {
        answer = resolve;
      });
    }) as typeof globalThis.fetch);
    const pending = getRuns({ limit: 1000, since: "360h" }, { timeoutMs: 60_000 });
    await Promise.resolve();
    t.mock.timers.tick(20_000);
    assert.equal(seen.signal?.aborted, false);
    answer(new Response(JSON.stringify({ runs: [{ id: "late" }] }), { status: 200 }));
    assert.deepEqual((await pending).map((r) => r.id), ["late"]);
  });
});
