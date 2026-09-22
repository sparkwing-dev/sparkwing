import assert from "node:assert/strict";
import { afterEach, before, beforeEach, describe, it } from "node:test";

type Teams = typeof import("./teams");
let teams!: Teams;

type Runtime = {
  window?: { __SPARKWING_REQUIRE_LOGIN__?: string };
  document?: { cookie: string };
  fetch: typeof fetch;
};
const runtime = globalThis as unknown as Runtime;

before(async () => {
  runtime.window = {};
  try {
    teams = await import("./teams");
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
      body: typeof init?.body === "string" ? init.body : "",
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

describe("teamsEnabled", () => {
  it("reads only an explicit teams.enabled from the controller", () => {
    assert.equal(teams.teamsEnabled(null), false);
    assert.equal(teams.teamsEnabled({ mode: "local" }), false);
    assert.equal(
      teams.teamsEnabled({ mode: "cluster", teams: { enabled: false } }),
      false,
    );
    assert.equal(
      teams.teamsEnabled({ mode: "cluster", teams: { enabled: true } }),
      true,
    );
  });
});

describe("role gates", () => {
  it("offers team administration to owners only", () => {
    assert.equal(teams.canManageTeam("owner"), true);
    assert.equal(teams.canManageTeam("editor"), false);
    assert.equal(teams.canManageTeam("reader"), false);
    assert.equal(teams.canManageTeam(undefined), false);
  });

  it("offers connecting a machine to editors and owners", () => {
    assert.equal(teams.canConnectMachines("owner"), true);
    assert.equal(teams.canConnectMachines("editor"), true);
    assert.equal(teams.canConnectMachines("reader"), false);
  });

  it("never offers a role above the caller's own", () => {
    assert.deepEqual(teams.assignableRoles("owner"), [
      "owner",
      "editor",
      "reader",
    ]);
    assert.deepEqual(teams.assignableRoles("editor"), ["editor", "reader"]);
    assert.deepEqual(teams.assignableRoles("reader"), ["reader"]);
    assert.deepEqual(teams.assignableRoles(undefined), []);
  });
});

describe("team slugs", () => {
  it("accepts DNS-safe slugs and names what is wrong with the rest", () => {
    assert.equal(teams.teamSlugProblem("acme-ci"), null);
    assert.match(teams.teamSlugProblem("Acme") ?? "", /lowercase/);
    assert.match(teams.teamSlugProblem("-acme") ?? "", /Start and end/);
    assert.match(teams.teamSlugProblem("a") ?? "", /2 to 40/);
    assert.match(teams.teamSlugProblem("acme.io") ?? "", /lowercase/);
  });

  it("derives a slug from a display name", () => {
    assert.equal(teams.slugFromName("Ada's Space"), "ada-s-space");
    assert.equal(teams.slugFromName("  Café Ops  "), "cafe-ops");
    assert.equal(
      teams.teamSlugProblem(teams.slugFromName("x".repeat(80))),
      null,
    );
  });
});

describe("switchTeam", () => {
  it("posts the slug with the session CSRF token", async () => {
    await teams.switchTeam("acme");
    assert.equal(calls.length, 1);
    assert.equal(calls[0].url, "/api/v1/me/active-team");
    assert.equal(calls[0].method, "POST");
    assert.deepEqual(JSON.parse(calls[0].body), { slug: "acme" });
    assert.equal(calls[0].headers.get("X-CSRF-Token"), "session-csrf");
  });

  it("surfaces the controller's refusal", async () => {
    respond = () =>
      new Response(JSON.stringify({ error: "not a member of acme" }), {
        status: 403,
      });
    await assert.rejects(teams.switchTeam("acme"), (err: Error) => {
      assert.match(err.message, /not a member of acme/);
      return true;
    });
  });
});

describe("getMe", () => {
  it("returns null rather than throwing when the controller has no /me", async () => {
    respond = () => new Response("not found", { status: 404 });
    assert.equal(await teams.getMe(), null);
  });
});
