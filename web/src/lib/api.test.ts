import assert from "node:assert/strict";
import { before, describe, it } from "node:test";

type RuntimeWindow = {
  __SPARKWING_REQUIRE_LOGIN__?: string;
  location?: { pathname: string; search: string; assign: (url: string) => void };
};
let getNodeStreamUrl!: typeof import("./api").getNodeStreamUrl;
let cancelRun!: typeof import("./api").cancelRun;
let getConnectionStatus!: typeof import("./api").getConnectionStatus;
let triggerRun!: typeof import("./api").triggerRun;

before(async () => {
  const runtime = globalThis as unknown as { window?: RuntimeWindow };
  const hadWindow = Object.prototype.hasOwnProperty.call(runtime, "window");
  const previousWindow = runtime.window;
  runtime.window = {};
  try {
    ({
      getNodeStreamUrl,
      cancelRun,
      getConnectionStatus,
      triggerRun,
    } = await import("./api"));
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

describe("triggerRun", () => {
  async function sentBody(
    git?: Parameters<typeof triggerRun>[2],
  ): Promise<Record<string, unknown>> {
    const runtime = globalThis as unknown as { fetch: typeof fetch };
    const previousFetch = runtime.fetch;
    let body = "";
    runtime.fetch = async (_input, init) => {
      body = String(init?.body ?? "");
      return new Response(JSON.stringify({ run_id: "run-one" }), { status: 202 });
    };
    try {
      await triggerRun("build", {}, git);
    } finally {
      runtime.fetch = previousFetch;
    }
    return JSON.parse(body) as Record<string, unknown>;
  }

  it("names the repository and branch a team runner fetches", async () => {
    const git = {
      repo_url: "https://github.com/acme/app",
      branch: "main",
      repo: "app",
      github_owner: "acme",
      github_repo: "app",
    };
    const body = await sentBody(git);
    assert.deepEqual(body.git, git);
    assert.deepEqual(body.trigger, { source: "dashboard" });
  });

  it("sends no git block when the form names no repository", async () => {
    const body = await sentBody();
    assert.equal("git" in body, false);
  });
});

describe("session expiry", () => {
  it("keeps the token banner when a token mode dashboard is refused", async () => {
    const runtime = globalThis as unknown as {
      window?: RuntimeWindow;
      document?: { cookie: string };
      fetch: typeof fetch;
    };
    const hadWindow = Object.prototype.hasOwnProperty.call(runtime, "window");
    const previousWindow = runtime.window;
    const previousFetch = runtime.fetch;
    const assigned: string[] = [];
    runtime.window = {
      __SPARKWING_REQUIRE_LOGIN__: "false",
      location: {
        pathname: "/capacity",
        search: "",
        assign: (url: string) => assigned.push(url),
      },
    };
    runtime.fetch = async () => new Response(null, { status: 401 });
    try {
      await assert.rejects(cancelRun("run-one"));
      assert.equal(getConnectionStatus(), "unauthorized");
      assert.deepEqual(assigned, []);
    } finally {
      runtime.fetch = previousFetch;
      if (hadWindow) runtime.window = previousWindow;
      else delete runtime.window;
    }
  });

  it("sends a capped out session mode tab to the sign-in page and stops polling", async () => {
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
    const assigned: string[] = [];
    let requests = 0;
    runtime.window = {
      __SPARKWING_REQUIRE_LOGIN__: "true",
      location: {
        pathname: "/capacity",
        search: "?window=1h",
        assign: (url: string) => assigned.push(url),
      },
    };
    runtime.document = { cookie: "sw_csrf=session-token" };
    runtime.fetch = async () => {
      requests += 1;
      return new Response(null, { status: 401 });
    };
    try {
      await assert.rejects(cancelRun("run-one"));
      assert.deepEqual(assigned, ["/login?next=%2Fcapacity%3Fwindow%3D1h"]);
      assert.equal(getConnectionStatus(), "session-expired");
      assert.equal(requests, 1);

      await assert.rejects(cancelRun("run-one"), /session ended/);
      assert.equal(requests, 1);
      assert.deepEqual(assigned, ["/login?next=%2Fcapacity%3Fwindow%3D1h"]);
    } finally {
      runtime.fetch = previousFetch;
      if (hadWindow) runtime.window = previousWindow;
      else delete runtime.window;
      if (hadDocument) runtime.document = previousDocument;
      else delete runtime.document;
    }
  });
});
