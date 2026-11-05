import assert from "node:assert/strict";
import { afterEach, before, beforeEach, describe, it } from "node:test";

type DeviceLoginLib = typeof import("./deviceLogin");
let lib!: DeviceLoginLib;

type Runtime = {
  window?: { __SPARKWING_REQUIRE_LOGIN__?: string };
  document?: { cookie: string };
  fetch: typeof fetch;
};
const runtime = globalThis as unknown as Runtime;

before(async () => {
  runtime.window = {};
  try {
    lib = await import("./deviceLogin");
  } finally {
    delete runtime.window;
  }
});

type Call = { url: string; method: string; headers: Headers; body: string };
let calls: Call[] = [];
let respond: (call: Call) => Response;
const realFetch = runtime.fetch;

beforeEach(() => {
  calls = [];
  respond = () => new Response(null, { status: 204 });
  runtime.window = { __SPARKWING_REQUIRE_LOGIN__: "true" };
  runtime.document = { cookie: "__Host-sw_csrf=session-csrf" };
  runtime.fetch = async (input, init) => {
    const call = {
      url: String(input),
      method: init?.method ?? "GET",
      headers: new Headers(init?.headers),
      body: String(init?.body ?? ""),
    };
    calls.push(call);
    return respond(call);
  };
});

afterEach(() => {
  delete runtime.window;
  delete runtime.document;
  runtime.fetch = realFetch;
});

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("normalizeDeviceCode", () => {
  it("accepts the code with or without the hyphen, in any case", () => {
    assert.equal(lib.normalizeDeviceCode("BCDF-GHJK"), "BCDFGHJK");
    assert.equal(lib.normalizeDeviceCode("bcdfghjk"), "BCDFGHJK");
    assert.equal(lib.normalizeDeviceCode(" bcdf ghjk\n"), "BCDFGHJK");
  });

  it("drops digits and punctuation and stops at eight letters", () => {
    assert.equal(lib.normalizeDeviceCode("b1c2d3f4-g.h_j k"), "BCDFGHJK");
    assert.equal(lib.normalizeDeviceCode("BCDFGHJKLM"), "BCDFGHJK");
  });
});

describe("formatDeviceCode", () => {
  it("puts the hyphen after the fourth letter once there is a fifth", () => {
    assert.equal(lib.formatDeviceCode(""), "");
    assert.equal(lib.formatDeviceCode("BCDF"), "BCDF");
    assert.equal(lib.formatDeviceCode("BCDFG"), "BCDF-G");
    assert.equal(lib.formatDeviceCode("BCDFGHJK"), "BCDF-GHJK");
  });
});

describe("deviceCodeComplete", () => {
  it("needs eight letters from the code alphabet", () => {
    assert.equal(lib.deviceCodeComplete("BCDFGHJK"), true);
    assert.equal(lib.deviceCodeComplete("BCDFGHJ"), false);
    assert.equal(lib.deviceCodeComplete("BCDFGHJA"), false);
    assert.equal(lib.deviceCodeComplete("BCDFGHJY"), false);
    assert.equal(lib.deviceCodeComplete("bcdfghjk"), false);
  });

  it("flags a letter no code uses", () => {
    assert.equal(lib.deviceCodeHasForeignLetter("BCDE"), true);
    assert.equal(lib.deviceCodeHasForeignLetter("BCDF"), false);
  });
});

describe("grantText", () => {
  it("explains each grant the console knows", () => {
    assert.equal(lib.grantText("runs.read"), "See runs");
    assert.equal(lib.grantText("runs.control"), "Cancel and retry runs");
    assert.equal(
      lib.grantText("claims.launch"),
      "Launch Cloud jobs for a reserved pool",
    );
  });

  it("shows an unknown grant by its name", () => {
    assert.equal(lib.grantText("secrets.write"), "secrets.write");
    assert.equal(lib.grantText("constructor"), "constructor");
  });
});

describe("expiryText", () => {
  const at = "2026-11-04T10:10:00Z";
  const base = Date.parse("2026-11-04T10:00:00Z");

  it("counts whole minutes left, rounded up", () => {
    assert.equal(lib.minutesLeft(at, base), 10);
    assert.equal(lib.minutesLeft(at, base + 9 * 60_000 + 1), 1);
    assert.equal(
      lib.expiryText(at, base + 9 * 60_000 + 1),
      "This request expires in about 1 minute.",
    );
    assert.equal(
      lib.expiryText(at, base),
      "This request expires in about 10 minutes.",
    );
  });

  it("says a past or unreadable time has expired", () => {
    assert.equal(
      lib.expiryText(at, base + 11 * 60_000),
      "This request has expired.",
    );
    assert.equal(lib.expiryText("", base), "This request has expired.");
  });
});

describe("device requests", () => {
  it("looks a code up with the session's CSRF token", async () => {
    respond = () =>
      json(200, {
        user_code: "BCDFGHJK",
        kind: "member",
        machine: "laptop",
        team: "acme",
        grants: ["runs.read"],
        created_at: "2026-11-04T10:00:00Z",
        expires_at: "2026-11-04T10:10:00Z",
      });
    const request = await lib.lookupDeviceRequest("BCDFGHJK");
    assert.equal(request.machine, "laptop");
    assert.equal(request.runner, undefined);
    assert.equal(calls[0].url, "/api/v1/auth/device/lookup");
    assert.equal(calls[0].method, "POST");
    assert.equal(calls[0].headers.get("X-CSRF-Token"), "session-csrf");
    assert.deepEqual(JSON.parse(calls[0].body), { user_code: "BCDFGHJK" });
  });

  it("fills a runner's missing lists", async () => {
    respond = () =>
      json(200, {
        kind: "runner",
        team: "acme",
        runner: { name: "box", personal: false },
      });
    const request = await lib.lookupDeviceRequest("BCDFGHJK");
    assert.deepEqual(request.runner?.repos, []);
    assert.deepEqual(request.runner?.reserved_pools, []);
    assert.deepEqual(request.grants, []);
  });

  it("words a 404 the same whatever the server says", async () => {
    respond = () =>
      json(404, {
        error: { code: "not_found", message: "no such device code" },
      });
    await assert.rejects(lib.approveDeviceRequest("BCDFGHJK"), (err: Error) => {
      assert.equal(err.message, lib.expiredCodeText);
      return true;
    });
    assert.equal(calls[0].url, "/api/v1/auth/device/approve");
  });

  it("shows a refusal's own message", async () => {
    respond = () =>
      json(403, {
        error: {
          code: "forbidden",
          message: "only a team admin can add a shared runner",
        },
      });
    await assert.rejects(lib.approveDeviceRequest("BCDFGHJK"), {
      message: "only a team admin can add a shared runner",
    });
  });

  it("names the action when a failure has no message", async () => {
    respond = () => new Response("", { status: 500 });
    await assert.rejects(lib.denyDeviceRequest("BCDFGHJK"), {
      message: "Deny failed (500)",
    });
    assert.equal(calls[0].url, "/api/v1/auth/device/deny");
  });

  // Last, because a 429 makes authFetch back off every later request.
  it("shows a rate limit's own message", async () => {
    respond = () =>
      json(429, {
        error: {
          code: "rate_limited",
          message: "too many codes tried; wait a minute",
        },
      });
    await assert.rejects(lib.lookupDeviceRequest("BCDFGHJK"), {
      message: "too many codes tried; wait a minute",
    });
  });
});
