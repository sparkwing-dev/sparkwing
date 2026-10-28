import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { before, describe, it } from "node:test";

type RuntimeWindow = {
  __SPARKWING_REQUIRE_LOGIN__?: string;
  location?: { pathname: string; search: string; assign: (url: string) => void };
};
let getNodeStreamUrl!: typeof import("./api").getNodeStreamUrl;
let cancelRun!: typeof import("./api").cancelRun;
let deleteRun!: typeof import("./api").deleteRun;
let getNodeLogs!: typeof import("./api").getNodeLogs;
let getConnectionStatus!: typeof import("./api").getConnectionStatus;
let triggerRun!: typeof import("./api").triggerRun;
let pipelinesByName!: typeof import("./api").pipelinesByName;
let parseRunArgs!: typeof import("./api").parseRunArgs;
let getNodeMetrics!: typeof import("./api").getNodeMetrics;
let getPaused!: typeof import("./api").getPaused;
let releaseNode!: typeof import("./api").releaseNode;

before(async () => {
  const runtime = globalThis as unknown as { window?: RuntimeWindow };
  const hadWindow = Object.prototype.hasOwnProperty.call(runtime, "window");
  const previousWindow = runtime.window;
  runtime.window = {};
  try {
    ({
      getNodeStreamUrl,
      cancelRun,
      deleteRun,
      getNodeLogs,
      getConnectionStatus,
      triggerRun,
      pipelinesByName,
      parseRunArgs,
      getNodeMetrics,
      getPaused,
      releaseNode,
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
      "/api/v1/runs/run-one/jobs/verify/logs/stream?format=ndjson",
    );
    assert.doesNotMatch(url, /format=ansi/);
  });
});

describe("run path segments", () => {
  async function requestedPaths(call: () => Promise<unknown>): Promise<string[]> {
    const runtime = globalThis as unknown as { fetch: typeof fetch };
    const previousFetch = runtime.fetch;
    const paths: string[] = [];
    runtime.fetch = async (input) => {
      paths.push(new URL(String(input), "https://dashboard.example").pathname);
      return new Response(null, { status: 204 });
    };
    try {
      await call();
    } finally {
      runtime.fetch = previousFetch;
    }
    return paths;
  }

  it("keeps a traversal-shaped run id inside the run's own path", async () => {
    for (const id of ["../team/members/x", "%2e%2e/team/members/x", "a/../../crons/c/run?"]) {
      const [path] = await requestedPaths(() => deleteRun(id));
      assert.match(path, /^\/api\/v1\/runs\/[^/]+$/, `deleteRun(${id}) requested ${path}`);
    }
    const [logs] = await requestedPaths(() => getNodeLogs("run-one", "../../team"));
    assert.match(logs, /^\/api\/v1\/runs\/run-one\/jobs\/[^/]+\/logs$/);
  });

  it("refuses a dot-segment run id", async () => {
    for (const id of ["..", ".", ""]) {
      const paths = await requestedPaths(() => deleteRun(id).catch(() => undefined));
      assert.deepEqual(paths, [], `deleteRun(${JSON.stringify(id)}) sent a request`);
      await assert.rejects(deleteRun(id));
    }
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
    runtime.fetch = async (input, init) => {
      assert.equal(String(input), "/api/v1/runs");
      assert.equal(init?.method, "POST");
      assert.equal(new Headers(init?.headers).get("Content-Type"), "application/json");
      body = String(init?.body ?? "");
      return new Response(JSON.stringify({ run_id: "run-one" }), { status: 202 });
    };
    try {
      assert.deepEqual(await triggerRun("build", { tag: "nightly" }, git), { run_id: "run-one" });
    } finally {
      runtime.fetch = previousFetch;
    }
    const envelope = JSON.parse(body) as Record<string, unknown>;
    assert.deepEqual(Object.keys(envelope).sort(), ["idempotency_key", "request"]);
    assert.match(String(envelope.idempotency_key), /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    return envelope;
  }

  it("submits the run envelope with repository display and source request fields", async () => {
    const git = {
      repo_url: "https://github.com/acme/app",
      branch: "main",
      repo: "app",
      github_owner: "acme",
      github_repo: "app",
    };
    const body = await sentBody(git);
    assert.deepEqual(body.request, {
      version: 1,
      pipeline: "build",
      mode: "execute",
      origin: "trigger",
      dispatch: "host",
      requested_args: { tag: "nightly" },
      git: { repository: { display: "https://github.com/acme/app" }, ref: "main" },
      source: { ref: "main", location: { remote_url: "https://github.com/acme/app", pipeline_dir: ".sparkwing" } },
    });
  });

  it("omits source request fields when the form names no repository", async () => {
    const body = await sentBody();
    assert.deepEqual(body.request, {
      version: 1,
      pipeline: "build",
      mode: "execute",
      origin: "trigger",
      dispatch: "host",
      requested_args: { tag: "nightly" },
    });
  });

  it("uses a fresh idempotency key for each submitted run", async () => {
    const first = await sentBody();
    const second = await sentBody();
    assert.notEqual(first.idempotency_key, second.idempotency_key);
  });
});

describe("reporting reads", () => {
  async function readErrors(call: () => Promise<unknown>, message: string) {
    const previousFetch = globalThis.fetch;
    try {
      for (const status of [404, 503]) {
        globalThis.fetch = async () => new Response(null, { status });
        await assert.rejects(call(), new RegExp(`${message}: ${status}`));
      }
      globalThis.fetch = async () => { throw new Error("offline"); };
      await assert.rejects(call(), /offline/);
    } finally {
      globalThis.fetch = previousFetch;
    }
  }

  it("propagates missing or unavailable metrics instead of returning no points", async () => {
    await readErrors(() => getNodeMetrics("run-one", "job"), "metrics failed");
  });

  it("propagates missing or unavailable debug pauses instead of returning none", async () => {
    await readErrors(() => getPaused("run-one"), "debug pauses failed");
  });
});

describe("releaseNode", () => {
  it("resumes the selected job with a JSON object", async () => {
    const previousFetch = globalThis.fetch;
    let url = "";
    let request: RequestInit | undefined;
    globalThis.fetch = async (input, init) => {
      url = String(input);
      request = init;
      return new Response(null, { status: 204 });
    };
    try {
      await releaseNode("run-one", "job/nested");
      assert.equal(url, "/api/v1/runs/run-one/jobs/job%2Fnested/resume");
      assert.equal(request?.method, "POST");
      assert.equal(new Headers(request?.headers).get("Content-Type"), "application/json");
      assert.deepEqual(JSON.parse(String(request?.body)), {});
    } finally {
      globalThis.fetch = previousFetch;
    }
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

  it("reports a permission refusal apart from missing authentication", async () => {
    const runtime = globalThis as unknown as { window?: RuntimeWindow; fetch: typeof fetch };
    const hadWindow = Object.prototype.hasOwnProperty.call(runtime, "window");
    const previousWindow = runtime.window;
    const previousFetch = runtime.fetch;
    for (const mode of ["true", "false"]) {
      runtime.window = { __SPARKWING_REQUIRE_LOGIN__: mode };
      runtime.fetch = async () => new Response(null, { status: 403 });
      try {
        await assert.rejects(cancelRun("run-one"));
        assert.equal(getConnectionStatus(), "forbidden");
      } finally {
        runtime.fetch = previousFetch;
        if (hadWindow) runtime.window = previousWindow;
        else delete runtime.window;
      }
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

describe("pipelinesByName", () => {
  it("keeps the controller's newest-first order and its last run", () => {
    const got = pipelinesByName(
      JSON.parse(readFileSync(join(__dirname, "testdata", "pipelines.json"), "utf8")).pipelines,
    );
    assert.deepEqual(Object.keys(got), ["hello"]);
    assert.deepEqual(got.hello, { last_status: "failed", last_run_at: "2026-10-28T05:01:19Z" });
  });
});

describe("parseRunArgs", () => {
  it("keeps each value exactly as typed after the first =", () => {
    assert.deepEqual(parseRunArgs("target=prod\n\nverbose=\nlabel=a=b\nmessage=  padded text  "), {
      ok: true,
      values: { target: "prod", verbose: "", label: "a=b", message: "  padded text  " },
    });
  });

  it("names a line that is not name=value and a repeated name, never an inherited one", () => {
    assert.deepEqual(parseRunArgs("prod"), { ok: false, problem: '"prod" is not name=value' });
    assert.deepEqual(parseRunArgs("a=1\na=2"), { ok: false, problem: "a is given twice" });
    assert.deepEqual(parseRunArgs("constructor=prod\ntoString=x"), {
      ok: true,
      values: { constructor: "prod", toString: "x" },
    });
  });
});