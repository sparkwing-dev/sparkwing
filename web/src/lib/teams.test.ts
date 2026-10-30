import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, join } from "node:path";
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

describe("billingEnabled", () => {
  it("offers billing only when controller capabilities enable it", () => {
    assert.equal(teams.billingEnabled(null), false);
    assert.equal(teams.billingEnabled({ mode: "cluster", teams: { enabled: true } }), false);
    assert.equal(teams.billingEnabled({ mode: "cluster", billing: { enabled: false } }), false);
    assert.equal(teams.billingEnabled({ mode: "cluster", billing: { enabled: true } }), true);
  });
});

describe("role gates", () => {
  it("offers team administration to owners only", () => {
    assert.equal(teams.canManageTeam("owner"), true);
    assert.equal(teams.canManageTeam("editor"), false);
    assert.equal(teams.canManageTeam("reader"), false);
    assert.equal(teams.canManageTeam(undefined), false);
  });

  it("offers connecting a machine to owners", () => {
    assert.equal(teams.canConnectMachines("owner"), true);
    assert.equal(teams.canConnectMachines("editor"), false);
    assert.equal(teams.canConnectMachines("reader"), false);
    assert.equal(teams.canConnectMachines("editor", true), true);
    assert.equal(teams.canConnectMachines("reader", true), true);
    assert.equal(teams.canConnectMachines(undefined, true), false);
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
      new Response(JSON.stringify({error: {code:"forbidden",message:"not a member of acme"}}), {
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

  it("reads a /me with no account as no team", async () => {
    respond = () =>
      new Response(JSON.stringify({ user: null, waitlisted: false }), {
        status: 200,
      });
    assert.deepEqual(await teams.getMe(), { kind: "operator" });
  });

  it("reads a waitlisted account with no team as waitlisted, not as the operator", async () => {
    const body = {
      user: { id: "u2", email: "w@x", name: "W" },
      active_team: null,
      memberships: [],
      invitations: [
        {
          id: "i1",
          team_slug: "acme",
          team_display_name: "Acme",
          role: "reader",
        },
      ],
      waitlisted: true,
    };
    respond = () => new Response(JSON.stringify(body), { status: 200 });
    assert.deepEqual(await teams.getMe(), {
      kind: "waitlisted",
      me: {
        user: body.user,
        memberships: [],
        invitations: body.invitations,
        waitlisted: true,
      },
    });
  });

  it("reads a waitlisted account that joined a team as a member", async () => {
    const me = {
      user: { id: "u3", email: "j@x", name: "J" },
      active_team: { slug: "acme", display_name: "Acme", role: "reader" },
      memberships: [],
      invitations: [],
      waitlisted: true,
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
    await teams.revokeTeamToken("swr_a/../../tokens");
    await teams.removeMember("u 1");
    await teams.acceptInvitation("inv?x=1");
    assert.deepEqual(
      calls.map((c) => `${c.method} ${c.url}`),
      [
        "DELETE /api/v1/tokens/swr_a%2F..%2F..%2Ftokens",
        "DELETE /api/v1/team/members/u%201",
        "POST /api/v1/invitations/inv%3Fx%3D1/accept",
      ],
    );
    for (const c of calls) {
      assert.equal(c.headers.get("X-CSRF-Token"), "session-csrf");
    }
  });

  it("says what a refused invitation means and what to do", async () => {
    respond = () =>
      new Response(
        JSON.stringify({
          error: {
            code: "conflict",
            message: "this invitation has expired or was already used",
            details: { reason: "invitation_closed" },
          },
        }),
        { status: 409 },
      );
    await assert.rejects(teams.acceptInvitation("inv1"), {
      message:
        "This invitation has expired or was already used. Ask a team owner to send a new one.",
    });
    respond = () =>
      new Response(
        JSON.stringify({ error: { code: "conflict", message: "account resource already exists" } }),
        { status: 409 },
      );
    await assert.rejects(teams.acceptInvitation("inv1"), {
      message: "Accept invitation: account resource already exists",
    });
  });

  it("keeps the configured member limit and remedy from an acceptance refusal", async () => {
    const message = "free team member limit reached: 3 of 3 members; ask a team owner to add funds or remove a member; support@sparkwing.dev";
    respond = () => new Response(JSON.stringify({ error: {
      code: "forbidden", message, details: { reason: "team_full" },
    } }), { status: 403 });
    await assert.rejects(teams.acceptInvitation("inv1"), {
      message: `Accept invitation: ${message}`,
      reason: "team_full",
    });
  });

  it("has recovery advice for each fixed invitation refusal", () => {
    const reasons = (
      JSON.parse(
        readFileSync(join(__dirname, "testdata", "refusal-reasons.json"), "utf8"),
      ) as { invitation: string[] }
    ).invitation;
    assert.deepEqual(
      Object.keys(teams.invitationRefusalText).sort(),
      reasons.filter((reason) => reason !== "team_full").sort(),
    );
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

describe("mintMachineToken", () => {
  it("keeps personal enrollment explicit", async () => {
    respond = () => Response.json({ capability: "private-machine", grants: ["nodes.claim"] });
    await teams.mintMachineToken("desk", ["github.com/acme/*"], true);
    assert.equal(JSON.parse(calls[0].body).personal, true);
  });
  it("sends the name and repository patterns for server-authorized enrollment", async () => {
    respond = () =>
      new Response(
        JSON.stringify({
          capability: "swk_x",
          prefix: "swk_x",
          team: "team",
          grants: ["nodes.claim", "triggers.claim"],
        }),
        { status: 201 },
      );
    await teams.mintMachineToken("build-box", [
      "github.com/acme/*",
      "github.com/acme/app",
    ]);
    assert.equal(calls[0].url, "/api/v1/runners");
    assert.equal(calls[0].method, "POST");
    assert.deepEqual(JSON.parse(calls[0].body), {
      name: "build-box",
      repos: ["github.com/acme/*", "github.com/acme/app"],
    });
  });

  it("surfaces the controller's pattern validation error", async () => {
    respond = () =>
      new Response(JSON.stringify({error: {code:"bad_request",message:"bad repo pattern"}}), {
        status: 400,
      });
    await assert.rejects(
      teams.mintMachineToken("build-box", ["not a pattern"]),
      (err: Error) => {
        assert.match(err.message, /bad repo pattern/);
        return true;
      },
    );
  });
});

describe("team settings", () => {
  it("reads and changes enrollment and child limits through the selected team's settings routes", async () => {
    respond = () => Response.json({ members_can_add_machines: true, max_children_per_root: 50, high_trust: false });
    assert.deepEqual(await teams.getTeamSettings(), { members_can_add_machines: true, max_children_per_root: 50, high_trust: false });
    await teams.setTeamSettings({ members_can_add_machines: false });
    assert.equal(calls[0].url, "/api/v1/team/settings");
    assert.equal(calls[1].method, "PUT");
    assert.equal(calls[1].headers.get("X-CSRF-Token"), "session-csrf");
    assert.deepEqual(JSON.parse(calls[1].body), { members_can_add_machines: false });
    await teams.setTeamSettings({ max_children_per_root: 200 });
    assert.deepEqual(JSON.parse(calls[2].body), { max_children_per_root: 200 });
    await teams.setTeamSettings({ high_trust: true });
    assert.deepEqual(JSON.parse(calls[3].body), { high_trust: true });
  });
});

describe("listTeamTokens", () => {
  it("reads member and machine rows from one route", async () => {
    const rows = [
      {
        prefix: "swk_a",
        team: "team",
        subject: { kind: "account", id: "acct-1" },
        audience: "controller",
        grants: ["runs.read"],
        created_by: "acct-1",
        issued_at: "2026-10-14T12:00:00Z",
        expires_at: "2026-10-15T12:00:00Z",
      },
      {
        prefix: "swk_b",
        team: "team",
        subject: { kind: "machine", id: "build-box" },
        audience: "controller",
        grants: ["nodes.claim"],
        created_by: "acct-1",
        issued_at: "2026-10-14T12:00:00Z",
        last_used_at: "2026-10-14T13:00:00Z",
      },
    ];
    respond = () => new Response(JSON.stringify({ tokens: rows }), { status: 200 });
    assert.deepEqual(await teams.listTeamTokens(), rows);
    assert.equal(calls[0].method, "GET");
    assert.equal(calls[0].url, "/api/v1/tokens");
  });
});

describe("memberLabel", () => {
  const members = [
    { user_id: "acct-1", email: "ada@example.com", name: "Ada", role: "owner" as const },
    { user_id: "acct-2", email: "bo@example.com", name: "", role: "editor" as const },
  ];

  it("names a member by name, then email", () => {
    assert.equal(teams.memberLabel("acct-1", members), "Ada");
    assert.equal(teams.memberLabel("acct-2", members), "bo@example.com");
  });

  it("shows nothing for an account that is not a member", () => {
    assert.equal(teams.memberLabel("6d1cb2fc408db4878a9e60af5596a67b", members), null);
    assert.equal(teams.memberLabel(undefined, members), null);
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
  it("uses the bearer only in its private credential-file setup", () => {
    const cmd = teams.runnerConnectCommand({ capability: "swk_secret", grants: ["nodes.claim", "triggers.claim"] }, "Korey's laptop", [
      "github.com/acme/*",
    ], "https://controller.example");
    const dir = '\"$HOME/.config/sparkwing/runner-credentials\"';
    assert.ok(cmd.includes("printf '%s' swk_secret"));
    const runner = cmd.slice(cmd.indexOf("sparkwing-runner runner"));
    assert.equal(runner.includes("swk_secret"), false);
    assert.ok(runner.includes(`--credentials-dir ${dir}`));
    assert.ok(runner.includes("--controller https://controller.example"));
    assert.ok(runner.includes("--allow-repo 'github.com/acme/*'"));
    assert.ok(runner.includes("--holder-prefix 'Korey'"));
  });

  it("runs only the claim loops its grants allow", () => {
    for (const grants of [["nodes.claim"], ["triggers.claim"], ["nodes.claim", "triggers.claim"]]) {
      const cmd = teams.runnerConnectCommand({ capability: "swk_private", grants }, "builder", ["github.com/acme/*"], "https://controller.example");
      assert.equal(cmd.includes("--also-claim-triggers"), grants.includes("triggers.claim"));
      assert.equal(cmd.includes("--claim-nodes=false"), !grants.includes("nodes.claim"));
    }
  });

  it("replaces a readable token file with a private one before starting the runner", () => {
    const home = mkdtempSync(join(tmpdir(), "runner-setup-"));
    try {
      const credentials = join(home, ".config", "sparkwing", "runner-credentials");
      mkdirSync(credentials, { recursive: true });
      const file = join(credentials, "agent-token");
      writeFileSync(file, "old", { mode: 0o644 });
      const bin = join(home, "bin");
      mkdirSync(bin);
      writeFileSync(join(bin, "sparkwing-runner"), '#!/bin/sh\nprintf "%s\\n" "$@" > "$HOME/argv"\n', { mode: 0o755 });
      const cmd = teams.runnerConnectCommand({ capability: "swk_private", grants: ["nodes.claim", "triggers.claim"] }, "builder", ["github.com/acme/*", "gitlab.example/team/repo"], "https://controller.example");
      const result = spawnSync("sh", ["-c", cmd], { env: { ...process.env, HOME: home, PATH: bin+delimiter+process.env.PATH }, encoding: "utf8", stdio: "inherit" });
      assert.equal(result.error, undefined);
      assert.equal(result.status, 0, result.stderr);
      assert.equal(statSync(file).mode & 0o777, 0o600);
      assert.equal(readFileSync(file, "utf8"), "swk_private");
      const args = readFileSync(join(home, "argv"), "utf8");
      assert.equal(args.includes("swk_private"), false);
      assert.ok(args.includes("github.com/acme/*\n"));
      assert.ok(args.includes("gitlab.example/team/repo\n"));
      assert.ok(args.includes("https://controller.example\n"));
    } finally {
      rmSync(home, { recursive: true, force: true });
    }
  });

  it("leaves the old token untouched and stops if setup cannot replace it", (t) => {
    if (process.getuid?.() === 0) { t.skip("root bypasses directory permissions"); return; }
    const home = mkdtempSync(join(tmpdir(), "runner-refusal-"));
    const credentials = join(home, ".config", "sparkwing", "runner-credentials");
    try {
      mkdirSync(credentials, { recursive: true });
      const file = join(credentials, "agent-token");
      writeFileSync(file, "old", { mode: 0o644 });
      const bin = join(home, "bin");
      mkdirSync(bin);
      writeFileSync(join(bin, "sparkwing-runner"), '#!/bin/sh\ntouch "$HOME/started"\n', { mode: 0o755 });
      chmodSync(credentials, 0o555);
      const cmd = teams.runnerConnectCommand({ capability: "swk_private", grants: ["nodes.claim"] }, "builder", ["github.com/acme/*"], "https://controller.example");
      const result = spawnSync("sh", ["-c", cmd], { env: { ...process.env, HOME: home, PATH: bin+delimiter+process.env.PATH }, encoding: "utf8", stdio: "inherit" });
      assert.equal(result.error, undefined);
      assert.notEqual(result.status, 0);
      assert.equal(readFileSync(file, "utf8"), "old");
      assert.equal(existsSync(join(home, "started")), false);
    } finally {
      chmodSync(credentials, 0o755);
      rmSync(home, { recursive: true, force: true });
    }
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

describe("member tokens", () => {
  it("mints grants through the session's authenticated token route", async () => {
    const minted = {
      capability: "swk_x",
      prefix: "new-prefix",
      team: "acme",
      grants: ["runs.read", "runs.write"],
      expires_at: "2100-01-01T00:00:00Z",
    };
    respond = () => new Response(JSON.stringify(minted), { status: 201 });
    assert.deepEqual(
      await teams.mintMemberToken(["runs.read", "runs.write"]),
      minted,
    );
    assert.equal(calls[0].url, "/api/v1/tokens");
    assert.equal(calls[0].method, "POST");
    assert.deepEqual(JSON.parse(calls[0].body), {
      grants: ["runs.read", "runs.write"],
    });
    assert.equal(calls[0].headers.get("X-CSRF-Token"), "session-csrf");
  });

  it("gives each role its member grants without team administration", () => {
    const read = ["runs.read", "logs.read"];
    const write = [...read, "runs.write", "runs.control", "approvals.write"];
    assert.deepEqual(teams.memberScopesForRole("reader"), read);
    assert.deepEqual(teams.memberScopesForRole("editor"), write);
    assert.deepEqual(teams.memberScopesForRole("owner"), write);
  });

  it("revokes by escaped prefix", async () => {
    await teams.revokeTeamToken("swu_a/../tokens");
    assert.equal(calls[0].method, "DELETE");
    assert.equal(calls[0].url, "/api/v1/tokens/swu_a%2F..%2Ftokens");
  });

  it("tells a read-only token from one that starts runs", () => {
    assert.equal(teams.cliTokenStartsRuns(["runs.read", "runs.write"]), true);
    assert.equal(teams.cliTokenStartsRuns(["runs.read"]), false);
    assert.equal(teams.cliTokenStartsRuns(undefined), false);
  });
});

describe("deletion", () => {
  const me = (role: "owner" | "editor", teamCount: number) => ({
    user: { id: "u", email: "ada@example.com", name: "Ada" },
    active_team: { slug: "acme", display_name: "Acme", role },
    memberships: Array.from({ length: teamCount }, (_, i) => ({
      slug: `t${i}`,
      display_name: `T${i}`,
      role,
    })),
    invitations: [],
  });

  it("offers deleting a team only to an owner who keeps another team", () => {
    assert.equal(teams.canDeleteTeam(me("owner", 2)), true);
    assert.equal(teams.canDeleteTeam(me("owner", 1)), false);
    assert.equal(teams.canDeleteTeam(me("editor", 2)), false);
  });

  it("matches a typed confirmation the way the controller compares it", () => {
    assert.equal(teams.confirmationMatches(" Acme ", "acme"), true);
    assert.equal(teams.confirmationMatches("acm", "acme"), false);
    assert.equal(teams.confirmationMatches("", ""), false);
  });

  it("sends the typed slug when deleting the team", async () => {
    respond = () =>
      Response.json(
        {
          slug: "acme",
          state: "pending",
          requested_at: 1,
          finished_at: null,
          attempts: 0,
          last_error: "",
        },
        { status: 202 },
      );
    const del = await teams.deleteTeam("acme");
    assert.equal(calls[0].method, "DELETE");
    assert.equal(calls[0].url, "/api/v1/team");
    assert.deepEqual(JSON.parse(calls[0].body), { confirm_slug: "acme" });
    assert.equal(calls[0].headers.get("X-CSRF-Token"), "session-csrf");
    assert.equal(del.state, "pending");
  });

});
