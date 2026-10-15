import assert from "node:assert/strict";
import { test } from "node:test";
import { NodeRoutes } from "../src/routes.ts";

test("bound routes carry hello credentials only in Authorization and reject redirects", async () => {
  const requests: [string, RequestInit][] = [];
  const routes = new NodeRoutes({ url: "http://127.0.0.1:12345", bearer: "pipe-only" }, async (url, options) => {
    requests.push([String(url), options!]);
    return new Response(JSON.stringify({ value: "secret", masked: true }));
  });
  assert.equal(await routes.secret("KEY", new AbortController().signal), "secret");
  assert.equal(requests[0]![0], "http://127.0.0.1:12345/node/v1/secrets/KEY");
  assert.deepEqual(requests[0]![1].headers, { accept: "application/json", authorization: "Bearer pipe-only" });
  assert.equal(requests[0]![1].redirect, "error");
});

test("canonical route errors preserve future codes and reject double envelopes", async () => {
  const signal = new AbortController().signal;
  const routes = new NodeRoutes(
    { url: "http://127.0.0.1:12345", bearer: "pipe-only" },
    async () =>
      new Response(
        JSON.stringify({ error: { code: "future_error", message: "refused", hint: "retry later", details: { reason: "future" } } }),
        { status: 409 },
      ),
  );
  await assert.rejects(
    routes.output("producer", signal),
    (e: any) => e.code === "future_error" && e.hint === "retry later" && e.details.reason === "future",
  );
  const bad = new NodeRoutes(
    { url: "http://127.0.0.1:12345", bearer: "pipe-only" },
    async () => new Response('{"error":{"error":{"code":"bad_request","message":"no"}}}', { status: 400 }),
  );
  await assert.rejects(bad.output("producer", signal), /invalid error object/);
});

test("child inputs are explicit, raw args win, and fresh repos default to main", async () => {
  const calls: { url: string; body?: any }[] = [];
  const routes = new NodeRoutes({ url: "http://127.0.0.1:12345", bearer: "pipe-only" }, async (url, options) => {
    calls.push({ url: String(url), ...(options?.body ? { body: JSON.parse(String(options.body)) } : {}) });
    return String(url).endsWith("/children")
      ? new Response('{"run_id":"child"}', { status: 201 })
      : new Response('{"status":"success","output":{"value":7}}');
  });
  assert.deepEqual(
    await routes.runPipeline(
      "build",
      "result",
      { repo: "owner/child", inputs: { count: 2, enabled: false, targets: ["one", "two"] }, args: { count: "3" } },
      new AbortController().signal,
    ),
    { value: 7 },
  );
  assert.deepEqual(calls[0]!.body, {
    pipeline: "build",
    repo: "owner/child",
    branch: "main",
    args: { count: "3", enabled: "false", targets: "one,two" },
  });
  assert.match(calls[1]!.url, /\/children\/child\/await\?job=result&wait_ms=60000$/);
});

test("cancelling a child wait does not send child cancellation", async () => {
  const paths: string[] = [];
  const abort = new AbortController();
  const routes = new NodeRoutes({ url: "http://127.0.0.1:12345", bearer: "pipe-only" }, async (url, options) => {
    paths.push(String(url));
    if (paths.length === 1) return new Response('{"run_id":"child"}', { status: 201 });
    abort.abort(new Error("stop waiting"));
    options?.signal?.throwIfAborted();
    throw new Error("unreachable");
  });
  await assert.rejects(routes.runPipeline("build", "result", {}, abort.signal), /stop waiting/);
  assert.equal(paths.length, 2);
  assert.ok(paths.every((path) => !path.includes("cancel")));
});

test("annotations, summaries and masks await delivery errors", async () => {
  const signal = new AbortController().signal;
  const bodies: any[] = [];
  const routes = new NodeRoutes({ url: "http://127.0.0.1:12345", bearer: "pipe-only" }, async (_url, options) => {
    bodies.push(JSON.parse(String(options?.body)));
    return new Response('{"error":{"code":"unavailable","message":"delivery failed"}}', { status: 503 });
  });
  await assert.rejects(routes.annotate({ message: "note", file: "file.ts", line: 1, endLine: 3, column: 2 }, signal), /delivery failed/);
  await assert.rejects(routes.summary("markdown", signal), /delivery failed/);
  await assert.rejects(routes.mask("secret", signal), /delivery failed/);
  assert.deepEqual(bodies, [
    { message: "note", file: "file.ts", line: 1, column: 2, end_line: 3 },
    { markdown: "markdown" },
    { value: "secret" },
  ]);
});

test("child timeout ends waiting, preserves prototype-shaped input keys, and rejects invalid durations", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  let entered!: () => void;
  const waiting = new Promise<void>((resolve) => {
    entered = resolve;
  });
  const paths: string[] = [];
  const routes = new NodeRoutes({ url: "http://127.0.0.1:12345", bearer: "pipe-only" }, async (url, options) => {
    paths.push(String(url));
    if (paths.length === 1) {
      const body = JSON.parse(String(options?.body));
      assert.equal(body.args["__proto__"], "kept");
      return new Response('{"run_id":"child"}', { status: 201 });
    }
    entered();
    return new Promise((_resolve, reject) =>
      options?.signal?.addEventListener("abort", () => reject(options.signal?.reason), { once: true }),
    );
  });
  const result = routes.runPipeline(
    "build",
    "result",
    { timeoutMs: 10, inputs: JSON.parse('{"__proto__":"kept"}') },
    new AbortController().signal,
  );
  const rejected = assert.rejects(result, /child wait timed out/);
  await waiting;
  t.mock.timers.tick(10);
  await rejected;
  assert.equal(paths.length, 2);
  for (const timeoutMs of [-1, NaN, 0.5])
    await assert.rejects(
      routes.runPipeline("build", "result", { timeoutMs }, new AbortController().signal),
      (e: any) => e.code === "invalid_args",
    );
});
