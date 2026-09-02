import assert from "node:assert/strict";
import { before, describe, it } from "node:test";

type RuntimeWindow = {
  __SPARKWING_REQUIRE_LOGIN__?: string;
};
let getNodeStreamUrl!: typeof import("./api").getNodeStreamUrl;
let cancelRun!: typeof import("./api").cancelRun;

before(async () => {
  const runtime = globalThis as unknown as { window?: RuntimeWindow };
  const hadWindow = Object.prototype.hasOwnProperty.call(runtime, "window");
  const previousWindow = runtime.window;
  runtime.window = {};
  try {
    ({ getNodeStreamUrl, cancelRun } = await import("./api"));
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

describe("authenticated mutations", () => {
  it("copies the session CSRF cookie into the unsafe-request header", async () => {
    const runtime = globalThis as unknown as {
      window?: RuntimeWindow;
      document?: { cookie: string };
      fetch: typeof fetch;
    };
    const hadWindow = Object.prototype.hasOwnProperty.call(runtime, "window");
    const previousWindow = runtime.window;
    const hadDocument = Object.prototype.hasOwnProperty.call(runtime, "document");
    const previousDocument = runtime.document;
    const previousFetch = runtime.fetch;
    let headers = new Headers();
    runtime.window = { __SPARKWING_REQUIRE_LOGIN__: "true" };
    runtime.document = { cookie: "theme=dark; sw_csrf=session-token" };
    runtime.fetch = async (_input, init) => {
      headers = new Headers(init?.headers);
      return new Response(null, { status: 204 });
    };
    try {
      await cancelRun("run-one");
      assert.equal(headers.get("X-CSRF-Token"), "session-token");
    } finally {
      runtime.fetch = previousFetch;
      if (hadWindow) runtime.window = previousWindow;
      else delete runtime.window;
      if (hadDocument) runtime.document = previousDocument;
      else delete runtime.document;
    }
  });

  it("preserves sessionless mutation headers", async () => {
    const runtime = globalThis as unknown as {
      window?: RuntimeWindow;
      document?: { cookie: string };
      fetch: typeof fetch;
    };
    const hadWindow = Object.prototype.hasOwnProperty.call(runtime, "window");
    const previousWindow = runtime.window;
    const hadDocument = Object.prototype.hasOwnProperty.call(runtime, "document");
    const previousDocument = runtime.document;
    const previousFetch = runtime.fetch;
    let headers = new Headers();
    runtime.window = { __SPARKWING_REQUIRE_LOGIN__: "false" };
    runtime.document = { cookie: "sw_csrf=ignored-token" };
    runtime.fetch = async (_input, init) => {
      headers = new Headers(init?.headers);
      return new Response(null, { status: 204 });
    };
    try {
      await cancelRun("run-one");
      assert.equal(headers.get("X-CSRF-Token"), null);
    } finally {
      runtime.fetch = previousFetch;
      if (hadWindow) runtime.window = previousWindow;
      else delete runtime.window;
      if (hadDocument) runtime.document = previousDocument;
      else delete runtime.document;
    }
  });
});
