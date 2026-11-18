import assert from "node:assert/strict";
import { afterEach, before, beforeEach, describe, it } from "node:test";

type Reseal = typeof import("./reseal");
let lib: Reseal;
const runtime = globalThis as unknown as { window?: { __SPARKWING_REQUIRE_LOGIN__?: string }; document?: { cookie: string }; fetch: typeof fetch };
const realFetch = runtime.fetch;
let response: () => Response;
let calls: { path: string; init: RequestInit | undefined }[];
const counts = { secrets: 4, git_credentials: 2, run_environments: 3, run_arguments: 6, plaintext_run_arguments: 1, settings: 1, run_event_subscriptions: 2, previous_key_only: 4, unreadable: null };

before(async () => {
  runtime.window = {};
  try { lib = await import("./reseal"); } finally { delete runtime.window; }
});
beforeEach(() => {
  calls = [];
  response = () => Response.json(counts);
  runtime.window = { __SPARKWING_REQUIRE_LOGIN__: "true" };
  runtime.document = { cookie: "__Host-sw_csrf=fake-csrf" };
  runtime.fetch = async (path, init) => {
    calls.push({ path: String(path), init });
    return response();
  };
});
afterEach(() => {
  runtime.fetch = realFetch;
  delete runtime.window;
  delete runtime.document;
});

describe("operator reseal API", () => {
  it("sends bodyless verify and rotate POSTs with session CSRF proof", async () => {
    assert.deepEqual(await lib.requestReseal(true), { ...counts, unreadable: 0 });
    await lib.requestReseal(false);
    assert.deepEqual(calls.map(({ path, init }) => ({ path, method: init?.method, csrf: new Headers(init?.headers).get("X-CSRF-Token"), body: init?.body, credentials: init?.credentials, redirect: init?.redirect })), [
      { path: "/api/v1/operator/secrets/rotate?verify=1", method: "POST", csrf: "fake-csrf", body: undefined, credentials: "same-origin", redirect: "error" },
      { path: "/api/v1/operator/secrets/rotate", method: "POST", csrf: "fake-csrf", body: undefined, credentials: "same-origin", redirect: "error" },
    ]);
  });

  it("retains only numeric counts, including the unreadable row count", async () => {
    response = () => Response.json({ ...counts, key: "private", arguments: { secret: "private" }, unreadable: [{ name: "private", team: "private" }] });
    const result = await lib.requestReseal(true);
    assert.deepEqual(result, { ...counts, unreadable: 1 });
    assert.equal(JSON.stringify(result).includes("private"), false);
  });

  it("rejects incomplete or malformed counts instead of permitting a write", async () => {
    for (const body of [null, {}, { ...counts, previous_key_only: -1 }, { ...counts, previous_key_only: "0" }, { ...counts, secrets: 0.5 }, { ...counts, unreadable: "private" }, { ...counts, unreadable: undefined }, { ...counts, run_arguments: Number.MAX_SAFE_INTEGER + 1 }]) {
      response = () => Response.json(body);
      await assert.rejects(lib.requestReseal(true), /Incomplete verification counts/);
    }
  });

  it("requires all three post-verification risk counts to be zero", async () => {
    const result = await lib.requestReseal(true);
    const clean = { ...result, previous_key_only: 0, plaintext_run_arguments: 0 };
    assert.equal(lib.resealVerified(clean), true);
    for (const key of ["previous_key_only", "plaintext_run_arguments", "unreadable"] as const) {
      assert.equal(lib.resealVerified({ ...clean, [key]: 1 }), false);
    }
  });

  it("explains stale sign-in without showing the backend error text", async () => {
    response = () => Response.json({ error: { message: "private", details: { reason: "reauth_required" } } }, { status: 403 });
    await assert.rejects(lib.requestReseal(true), { message: "Sign out, sign in again, then retry within 10 minutes." });
  });

  it("handles missing authority, unavailable encryption and server errors without raw values", async () => {
    for (const [status, reason, message] of [
      [401, "", /Sign out, sign in again/],
      [403, "", /operator console session and valid CSRF proof/],
      [503, "secrets_key_required", /Secret encryption is unavailable/],
      [500, "", /HTTP 500/],
    ] as const) {
      response = () => Response.json({ error: { message: "private", details: { reason } } }, { status });
      await assert.rejects(lib.requestReseal(true), (err: Error) => message.test(err.message) && !err.message.includes("private"));
    }
  });

  it("reports an unknown outcome when the connection fails", async () => {
    response = () => { throw new Error("private"); };
    await assert.rejects(lib.requestReseal(false), { message: "The request did not finish. Its outcome is unknown. Run Verify secrets before trying again." });
  });
});
