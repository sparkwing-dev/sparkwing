import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { FetchEventStream, parseEventBlock } from "./eventStream";
import { signInCodeFrom } from "./localSession";

describe("parseEventBlock", () => {
  it("reads a named event and joins its data lines", () => {
    assert.deepEqual(parseEventBlock("id: 7\nevent: node_started\ndata: {\"a\":1}\ndata: more"), {
      type: "node_started",
      data: "{\"a\":1}\nmore",
      id: "7",
    });
  });
  it("defaults to a message and skips comments", () => {
    assert.deepEqual(parseEventBlock(": keepalive\ndata:line"), { type: "message", data: "line" });
  });
  it("carries an id or retry delay from a block with no data", () => {
    assert.deepEqual(parseEventBlock(": keepalive"), { type: "message", data: null });
    assert.deepEqual(parseEventBlock("id: 9\nretry: 250"), { type: "message", data: null, id: "9", retry: 250 });
  });
});

describe("signInCodeFrom", () => {
  it("reads the code from the fragment only", () => {
    assert.equal(signInCodeFrom("#code=abc%2Fdef"), "abc/def");
    assert.equal(signInCodeFrom("#x=1&code=zz"), "zz");
    assert.equal(signInCodeFrom(""), null);
    assert.equal(signInCodeFrom("#codex=1"), null);
  });
});

function streamOf(chunks: string[], hold: boolean): Response {
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(new TextEncoder().encode(chunk));
      if (!hold) controller.close();
    },
  });
  return new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } });
}

describe("FetchEventStream", () => {
  it("resumes from the last event id after the stream drops mid-run", async () => {
    const previousFetch = globalThis.fetch;
    const lastIds: (string | null)[] = [];
    globalThis.fetch = (async (_url: string, init?: RequestInit) => {
      const headers = new Headers(init?.headers);
      lastIds.push(headers.get("Last-Event-ID"));
      if (lastIds.length === 1) {
        return streamOf(["id: 1\nevent: node_started\ndata: build\n\nid: 2\nevent: node_succ", ""], false);
      }
      if (lastIds.length === 2) throw new TypeError("network down");
      return streamOf(["id: 2\nevent: node_succeeded\ndata: build\n\nevent: stream_end\ndata: {}\n\n"], true);
    }) as typeof fetch;
    try {
      const seen: string[] = [];
      let errors = 0;
      await new Promise<void>((resolve) => {
        const stream = new FetchEventStream("/api/v1/runs/r/events/stream", { initialRetryMs: 1 });
        stream.onerror = () => errors++;
        for (const kind of ["node_started", "node_succeeded"]) {
          stream.addEventListener(kind, (e) => seen.push(`${kind}:${e.data}`));
        }
        stream.addEventListener("stream_end", () => {
          stream.close();
          resolve();
        });
      });
      assert.deepEqual(seen, ["node_started:build", "node_succeeded:build"]);
      assert.deepEqual(lastIds, [null, "1", "1"]);
      assert.equal(errors, 2);
    } finally {
      globalThis.fetch = previousFetch;
    }
  });

  it("stops without retrying when the server refuses the stream", async () => {
    const previousFetch = globalThis.fetch;
    let calls = 0;
    globalThis.fetch = (async () => {
      calls++;
      return new Response("unauthorized", { status: 401 });
    }) as typeof fetch;
    try {
      await new Promise<void>((resolve) => {
        const stream = new FetchEventStream("/api/v1/runs/r/events/stream", { initialRetryMs: 1 });
        stream.onerror = () => resolve();
      });
      await new Promise((resolve) => setTimeout(resolve, 20));
      assert.equal(calls, 1);
    } finally {
      globalThis.fetch = previousFetch;
    }
  });
});
