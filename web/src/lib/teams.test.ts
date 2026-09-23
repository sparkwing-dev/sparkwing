import assert from "node:assert/strict";
import { afterEach, before, beforeEach, describe, it } from "node:test";

type Teams = typeof import("./teams");
let teams!: Teams;

type Runtime = {
  window?: {
    __SPARKWING_REQUIRE_LOGIN__?: string;
    location?: {
      pathname: string;
      search: string;
      assign: (url: string) => void;
    };
  };
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

describe("isLastOwner", () => {
  const ada = { user_id: "ada", role: "owner" as const };
  const grace = { user_id: "grace", role: "editor" as const };
  it("marks the sole owner and no one else", () => {
    assert.equal(teams.isLastOwner([ada, grace], "ada"), true);
    assert.equal(teams.isLastOwner([ada, grace], "grace"), false);
  });
  it("frees an owner once another owner exists", () => {
    const linus = { user_id: "linus", role: "owner" as const };
    assert.equal(teams.isLastOwner([ada, grace, linus], "ada"), false);
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
  it("reads a password operator's refused /me as no team, never as a signed-out session", async () => {
    const assigned: string[] = [];
    runtime.window = {
      __SPARKWING_REQUIRE_LOGIN__: "true",
      location: {
        pathname: "/",
        search: "",
        assign: (url: string) => assigned.push(url),
      },
    };
    const { getConnectionStatus } = await import("./api");
    for (const status of [401, 403, 404]) {
      respond = () => new Response("authentication required", { status });
      assert.deepEqual(await teams.getMe(), { kind: "operator" });
    }
    assert.deepEqual(assigned, [], "a /me refusal sent the tab to sign-in");
    assert.notEqual(getConnectionStatus(), "session-expired");

    respond = () => new Response("[]", { status: 200 });
    await teams.listMembers();
    assert.equal(calls.at(-1)?.url, "/api/v1/team/members");
  });

  it("reads an operator answer from the controller as no team", async () => {
    respond = () =>
      new Response(JSON.stringify({ user: null, operator: true }), {
        status: 200,
      });
    assert.deepEqual(await teams.getMe(), { kind: "operator" });
  });

  it("reads a member's /me", async () => {
    const me = {
      user: { id: "u1", email: "a@x", name: "A" },
      active_team: { slug: "acme", display_name: "Acme", role: "owner" },
      memberships: [],
      invitations: [],
    };
    respond = () => new Response(JSON.stringify(me), { status: 200 });
    assert.deepEqual(await teams.getMe(), { kind: "member", me });
  });
});

describe("team administration calls", () => {
  it("invites with email and role and returns the accept link", async () => {
    respond = () =>
      new Response(
        JSON.stringify({
          id: "inv1",
          accept_url: "https://d.example/invitations?id=inv1",
        }),
        { status: 201 },
      );
    const inv = await teams.inviteMember("new@example.com", "editor");
    assert.equal(inv.accept_url, "https://d.example/invitations?id=inv1");
    assert.equal(calls[0].url, "/api/v1/team/invitations");
    assert.equal(calls[0].method, "POST");
    assert.deepEqual(JSON.parse(calls[0].body), {
      email: "new@example.com",
      role: "editor",
    });
    assert.equal(calls[0].headers.get("X-CSRF-Token"), "session-csrf");
  });

  it("escapes path segments so an id cannot reach another route", async () => {
    await teams.revokeRunnerToken("swr_a/../../tokens");
    await teams.removeMember("u 1");
    await teams.acceptInvitation("inv?x=1");
    assert.deepEqual(
      calls.map((c) => `${c.method} ${c.url}`),
      [
        "DELETE /api/v1/team/runner-tokens/swr_a%2F..%2F..%2Ftokens",
        "DELETE /api/v1/team/members/u%201",
        "POST /api/v1/invitations/inv%3Fx%3D1/accept",
      ],
    );
    for (const c of calls) {
      assert.equal(c.headers.get("X-CSRF-Token"), "session-csrf");
    }
  });

  it("creates a team with slug and display name", async () => {
    respond = () =>
      new Response(JSON.stringify({ slug: "acme", display_name: "Acme" }), {
        status: 201,
      });
    const team = await teams.createTeam("acme", "Acme");
    assert.equal(team.slug, "acme");
    assert.deepEqual(JSON.parse(calls[0].body), {
      slug: "acme",
      display_name: "Acme",
    });
  });

  it("reads a list whether or not the controller wraps it", async () => {
    const rows = [{ user_id: "u1", email: "a@x", name: "A", role: "owner" }];
    respond = () => new Response(JSON.stringify(rows), { status: 200 });
    assert.deepEqual(await teams.listMembers(), rows);
    respond = () =>
      new Response(JSON.stringify({ members: rows }), { status: 200 });
    assert.deepEqual(await teams.listMembers(), rows);
    assert.deepEqual(teams.asList(null, "members"), []);
  });
});

describe("mintRunnerToken", () => {
  it("sends the name and repo patterns", async () => {
    respond = () =>
      new Response(
        JSON.stringify({ token: "swr_x", prefix: "swr_x", command: "" }),
        { status: 201 },
      );
    await teams.mintRunnerToken("build-box", [
      "github.com/acme/*",
      "github.com/acme/app",
    ]);
    assert.equal(calls[0].url, "/api/v1/team/runner-tokens");
    assert.equal(calls[0].method, "POST");
    assert.deepEqual(JSON.parse(calls[0].body), {
      name: "build-box",
      repos: ["github.com/acme/*", "github.com/acme/app"],
    });
  });

  it("surfaces the controller's pattern validation error", async () => {
    respond = () =>
      new Response(JSON.stringify({ error: "bad repo pattern" }), {
        status: 400,
      });
    await assert.rejects(
      teams.mintRunnerToken("build-box", ["not a pattern"]),
      (err: Error) => {
        assert.match(err.message, /bad repo pattern/);
        return true;
      },
    );
  });
});

describe("parseRepoPatterns", () => {
  it("splits on commas, whitespace and newlines and drops empties", () => {
    assert.deepEqual(
      teams.parseRepoPatterns(
        "github.com/acme/*, github.com/acme/app\n  github.com/other/*  ,,\t",
      ),
      ["github.com/acme/*", "github.com/acme/app", "github.com/other/*"],
    );
  });

  it("reads an empty or whitespace-only field as no patterns", () => {
    assert.deepEqual(teams.parseRepoPatterns(""), []);
    assert.deepEqual(teams.parseRepoPatterns("   \n  "), []);
  });
});

describe("runnerConnectCommand", () => {
  const minted = { token: "swr_secret", prefix: "swr_sec", command: "" };

  it("prefers the command the controller composed", () => {
    assert.equal(
      teams.runnerConnectCommand(
        {
          ...minted,
          command: "sparkwing-runner runner --controller https://c",
        },
        "box",
        ["github.com/acme/*"],
      ),
      "sparkwing-runner runner --controller https://c",
    );
  });

  it("falls back to the runner command with the token in the environment", () => {
    const cmd = teams.runnerConnectCommand(minted, "Korey's laptop", [
      "github.com/acme/*",
    ]);
    assert.equal(
      cmd,
      `SPARKWING_AGENT_TOKEN=swr_secret sparkwing-runner runner --controller ${teams.controllerURLPlaceholder} --allow-repo 'github.com/acme/*' --also-claim-triggers --max-claims-before-restart 0 --metrics-addr= --holder-prefix 'Korey'\\''s laptop'`,
    );
  });
});

describe("unixSecondsISO", () => {
  it("reads the controller's unix seconds as a date in its own year, not 1970", () => {
    assert.equal(
      teams.unixSecondsISO(1_790_000_000),
      "2026-09-21T14:13:20.000Z",
    );
  });
  it("leaves an absent stamp empty", () => {
    assert.equal(teams.unixSecondsISO(undefined), "");
    assert.equal(teams.unixSecondsISO(0), "");
  });
});
