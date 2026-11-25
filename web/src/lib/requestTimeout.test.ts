import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { describe, it, type TestContext } from "node:test";
import { authFetch, getConnectionStatus, getPendingApprovals, getRunners, getRuns } from "./api";

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

  it("ends a polled read whose body stalls after its headers at the deadline", { timeout: 5_000 }, async (t) => {
    const stalled: ServerResponse[] = [];
    const server = createServer((_request, response) => {
      stalled.push(response);
      response.writeHead(200, { "Content-Type": "application/json" });
      response.write('{"runs":[');
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    t.after(() => {
      for (const response of stalled) response.destroy();
      server.close();
    });
    const { port } = server.address() as AddressInfo;
    const realFetch = globalThis.fetch;
    withFetch(t, ((input: string, init?: RequestInit) =>
      realFetch(`http://127.0.0.1:${port}${input}`, init)) as typeof globalThis.fetch);
    await assert.rejects(
      getRuns({}, { timeoutMs: 300 }),
      (err: Error) => err.name === "RequestTimeoutError",
    );
  });

  it("reports a Fleet inventory timeout instead of an empty fleet", async (t) => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    withFetch(t, hangUntilAborted({}) as typeof globalThis.fetch);
    const pending = getRunners();
    t.mock.timers.tick(10_000);
    await assert.rejects(pending, (err: Error) => err.name === "RequestTimeoutError");
  });

  it("reports a body that a browser aborts at the deadline as the timeout", async (t) => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    withFetch(t, stallBodyLikeChromium('{"runners":['));
    const pending = getRunners();
    await settle();
    t.mock.timers.tick(10_000);
    await assert.rejects(pending, (err: Error) => err.name === "RequestTimeoutError");
  });

  it("ends a stalled approvals body at the deadline", async (t) => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    withFetch(t, stallBodyLikeChromium('{"approvals":['));
    const pending = getPendingApprovals();
    await settle();
    t.mock.timers.tick(10_000);
    assert.deepEqual(await pending, []);
  });

  it("leaves no deadline timer armed once a read settles", async (t) => {
    const armed = trackTimers(t);
    withFetch(t, (async () =>
      new Response(JSON.stringify({ runs: [{ id: "one" }] }), { status: 200 })) as typeof globalThis.fetch);
    await getRuns();
    assert.equal(armed.size, 0, "deadline still armed after a successful read");
    withFetch(t, (async () => new Response("unavailable", { status: 503 })) as typeof globalThis.fetch);
    await getRuns();
    assert.equal(armed.size, 0, "deadline still armed after an error status");
    withFetch(t, (async () => {
      throw new TypeError("network down");
    }) as typeof globalThis.fetch);
    await assert.rejects(getRuns());
    assert.equal(armed.size, 0, "deadline still armed after an early failure");
  });
});

async function settle() {
  for (let i = 0; i < 10; i++) await Promise.resolve();
}

// Chromium errors a stalled body with a plain AbortError, whatever the signal's reason.
function stallBodyLikeChromium(prefix: string): typeof globalThis.fetch {
  return (async (_input: unknown, init?: RequestInit) => {
    const body = new ReadableStream<Uint8Array>({
      start(stream) {
        stream.enqueue(new TextEncoder().encode(prefix));
        init?.signal?.addEventListener("abort", () =>
          stream.error(new DOMException("The user aborted a request.", "AbortError")),
        );
      },
    });
    return new Response(body, { status: 200, headers: { "Content-Type": "application/json" } });
  }) as typeof globalThis.fetch;
}

function trackTimers(t: TestContext): Set<unknown> {
  const armed = new Set<unknown>();
  const realSet = globalThis.setTimeout;
  const realClear = globalThis.clearTimeout;
  globalThis.setTimeout = ((handler: () => void, ms?: number) => {
    const id = realSet(() => {
      armed.delete(id);
      handler();
    }, ms);
    armed.add(id);
    return id;
  }) as typeof globalThis.setTimeout;
  globalThis.clearTimeout = ((id?: Parameters<typeof clearTimeout>[0]) => {
    armed.delete(id);
    realClear(id);
  }) as typeof globalThis.clearTimeout;
  t.after(() => {
    globalThis.setTimeout = realSet;
    globalThis.clearTimeout = realClear;
    for (const id of armed) realClear(id as Parameters<typeof clearTimeout>[0]);
  });
  return armed;
}
