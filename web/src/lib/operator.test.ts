import assert from "node:assert/strict";
import { afterEach, before, beforeEach, describe, it } from "node:test";

type OperatorLib = typeof import("./operator");
let lib!: OperatorLib;

type Runtime = {
  window?: { __SPARKWING_REQUIRE_LOGIN__?: string };
  document?: { cookie: string };
  fetch: typeof fetch;
};
const runtime = globalThis as unknown as Runtime;

before(async () => {
  runtime.window = {};
  try {
    lib = await import("./operator");
  } finally {
    delete runtime.window;
  }
});

type Call = { url: string; method: string; headers: Headers; body: string };
let calls: Call[] = [];
let respond: () => Response;
const realFetch = runtime.fetch;

beforeEach(() => {
  calls = [];
  respond = () => Response.json({});
  runtime.window = { __SPARKWING_REQUIRE_LOGIN__: "true" };
  runtime.document = { cookie: "__Host-sw_csrf=session-csrf" };
  runtime.fetch = async (input, init) => {
    calls.push({
      url: String(input),
      method: init?.method ?? "GET",
      headers: new Headers(init?.headers),
      body: typeof init?.body === "string" ? init.body : "",
    });
    return respond();
  };
});

afterEach(() => {
  delete runtime.window;
  delete runtime.document;
  runtime.fetch = realFetch;
});

const team = {
  team: "acme",
  display_name: "Acme",
  owners: ["olga@example.com"],
  balance_micro: 1_999_999,
  billing: {
    trust: "automatic" as const,
    trusted: false,
    standing:"New",cloud_vcpu:8,max_job_seconds:14400,
  },
  frozen: true,
  holds: ["dp_1", "operator-x"],
  hold_causes: ["dispute", "operator"],
  events: [],
};

describe("review", () => {
  it("needs a reason for every action", () => {
    for (const kind of Object.keys(lib.actionLabels)) {
      assert.match(
        lib.actionProblem(kind as never, "  ", "10"),
        /Give a reason/,
        kind,
      );
    }
    assert.equal(lib.actionProblem("freeze", "abuse", ""), "");
  });

  it("accepts positive grants above the retired caps", () => {
 assert.equal(lib.actionProblem("grant-credits","promo","6000"),"");
 assert.match(lib.actionProblem("grant-credits","promo","0"),/positive/);
 });

  it("names the team and the effect", () => {
    assert.equal(
      lib.actionEffect(team, "grant-credits", "20"),
      "Acme receives $20 of free credit.",
    );
    assert.match(
      lib.actionEffect(team, "unfreeze", ""),
      /The 1 operator hold\(s\) on Acme are released\. 1 payment hold\(s\) stay/,
    );
    assert.match(lib.actionEffect(team, "freeze", ""), /running work finishes/);
  });

  it("offers no limit on a revoked team and releases only operator holds", () => {
    const revoked = { ...team, billing: { ...team.billing, trust: "revoked" as const } };
    assert.deepEqual(lib.availableActions(revoked), [
      "grant-trust",
      "reset-trust",
      "grant-credits",
      "unfreeze",
      "release-hold",
    ]);
    assert.ok(!Object.keys(lib.actionLabels).includes("set-limit"));
    const disputed = { ...team, holds: ["dp_1"], hold_causes: ["dispute"] };
    assert.deepEqual(lib.availableActions(disputed).slice(-2), [
      "freeze",
      "release-hold",
    ]);
    const clear = { ...team, holds: [], hold_causes: [] };
    assert.ok(!lib.availableActions(clear).includes("release-hold"));
  });

  it("releases one dispute hold by name", () => {
    assert.match(
      lib.actionProblem("release-hold", "won", "", {}),
      /Choose the hold/,
    );
    assert.equal(lib.actionProblem("release-hold", "won", "", { hold: "dp_1" }), "");
    assert.match(
      lib.actionEffect(team, "release-hold", "", { hold: "dp_1" }),
      /The payment dispute dp_1 on Acme is released\. 1 other hold\(s\) stay/,
    );
    assert.deepEqual(
      lib.actionRequest("acme", "release-hold", "won", "", "", { hold: "dp_1" }),
      {
        path: "/api/v1/operator/teams/acme/unfreeze",
        body: { reason: "won", hold: "dp_1" },
      },
    );
  });

  it("truncates the balance to cents", () => {
    assert.equal(lib.balanceCents(1_999_999), 1);
  });
});

describe("confirmed action", () => {
  it("maps each action to its controller call", () => {
    const req = (kind: string, amount = "") =>
      lib.actionRequest("a b", kind as never, " why ", amount, "k1");
    assert.deepEqual(req("reset-trust"), {
      path: "/api/v1/operator/teams/a%20b/trust",
      body: { trust: "automatic", reason: "why" },
    });
    assert.deepEqual(req("grant-credits", "20.5"), {
      path: "/api/v1/operator/teams/a%20b/grants",
      body: { amount_cents: 2_050, reason: "why", key: "k1" },
    });
    assert.equal(req("revoke-trust").body.trust, "revoked");
    assert.equal(req("freeze").path, "/api/v1/operator/teams/a%20b/freeze");
    assert.equal(req("unfreeze").path, "/api/v1/operator/teams/a%20b/unfreeze");
  });

  it("posts with the session's CSRF token and surfaces a refusal", async () => {
    await lib.performAction(
      lib.actionRequest("acme", "freeze", "abuse", "", ""),
    );
    assert.equal(calls[0].method, "POST");
    assert.equal(calls[0].headers.get("X-CSRF-Token"), "session-csrf");
    assert.deepEqual(JSON.parse(calls[0].body), { reason: "abuse" });

    respond = () =>
      Response.json(
        {
          error: { code: "forbidden", message:
            "the operator console needs the operator's own signed-in account" },
        },
        { status: 403 },
      );
    await assert.rejects(
      lib.performAction(lib.actionRequest("acme", "freeze", "abuse", "", "")),
      /operator's own signed-in account/,
    );
  });
});

describe("team audit events", () => {
  it("follows the response cursor through the browser client", async () => {
    const firstEvent = { at: 2, kind: "team.mutated", subject_id: "account-1", attrs: { operation: "membership_removed" } };
    const olderEvent = { at: 1, kind: "run.deleted", run_id: "run-1" };
    respond = () => calls.length === 1
      ? Response.json({ ...team, events: [firstEvent], next_cursor: "fact:a/b+" })
      : Response.json({ ...team, events: [olderEvent] });
    const first = await lib.getOperatorTeam("a b");
    assert.equal(calls[0].url, "/api/v1/operator/teams/a%20b");
    assert.deepEqual(first.events, [firstEvent]);
    assert.equal(first.next_cursor, "fact:a/b+");
    const older = await lib.getOperatorTeam("a b", first.next_cursor);
    assert.equal(calls[1].url, "/api/v1/operator/teams/a%20b?cursor=fact%3Aa%2Fb%2B");
    assert.deepEqual(older.events, [olderEvent]);
    assert.equal(older.next_cursor, undefined);
  });
});

describe("waitlist", () => {
  it("approves by id with the session's CSRF token", async () => {
    respond = () =>
      Response.json({
        approved: [{ id: "a1", email: "ann@example.com", active_team: "ann" }],
      });
    assert.deepEqual(await lib.approveWaitlisted(["a1", "b2"]), [
      "ann@example.com",
    ]);
    assert.equal(calls[0].url, "/api/v1/operator/waitlist/approve");
    assert.equal(calls[0].method, "POST");
    assert.equal(calls[0].headers.get("X-CSRF-Token"), "session-csrf");
    assert.ok(calls[0].headers.get("Idempotency-Key"));
    assert.deepEqual(JSON.parse(calls[0].body), { account_ids: ["a1", "b2"] });
  });

  it("reads a page by offset", async () => {
    respond = () => Response.json({ accounts: [], total: 7 });
    assert.equal((await lib.getWaitlist(50)).total, 7);
    assert.equal(calls[0].url, "/api/v1/operator/waitlist?limit=50&offset=50");
  });
});

describe("standing requests", () => {
  it("reads the queue and declines with a reason", async () => {
    respond = () => Response.json({ requests: [{ request: { id: "standing-1", team: "a b", tier: "High", message: "m", requested_at: 1, state: "open" }, display_name: "A", standing: "Trusted", prepay_ceiling: "High", balance_micro: 0, history: { tier: "Trusted", paid_micro: 0 } }] });
    const queue = await lib.getStandingRequests();
    assert.equal(queue[0].request.id, "standing-1");
    assert.equal(calls[0].url, "/api/v1/operator/standing-requests");
    await lib.declineStandingRequest("a b", "standing-1", "  Pay for 30 days first.  ");
    assert.equal(calls[1].url, "/api/v1/operator/teams/a%20b/standing-request/decline");
    assert.equal(calls[1].method, "POST");
    assert.equal(calls[1].headers.get("X-CSRF-Token"), "session-csrf");
    assert.deepEqual(JSON.parse(calls[1].body), { request: "standing-1", reason: "Pay for 30 days first." });
  });

  it("approves through the standing grant", async () => {
    await lib.approveStandingRequest("acme", "standing-1", "Known customer");
    assert.equal(calls[0].url, "/api/v1/operator/teams/acme/trust");
    assert.deepEqual(JSON.parse(calls[0].body), { trust: "granted", reason: "Known customer", standing_request: "standing-1" });
  });

  it("summarizes payment history and clean days", () => {
    const now = Date.UTC(2026, 9, 31);
    const first = Date.UTC(2026, 9, 1) / 1000;
    assert.equal(
      lib.historyFacts({ tier: "Established", paid_micro: 50_000_000_000, first_paid_at: first }, now),
      "Established history · $500 paid · 30 clean days",
    );
    assert.equal(
      lib.historyFacts({ tier: "Established", paid_micro: 50_000_000_000, first_paid_at: first, last_incident_at: first + 20 * 86400 }, now),
      "Established history · $500 paid · 10 clean days",
    );
    assert.equal(lib.historyFacts({ tier: "New", paid_micro: 0 }, now), "New history · $0 paid · no payments");
  });
});

describe("hold causes and no-fault releases", () => {
  it("labels each payment hold by its recorded cause", () => {
    const held = { ...team, holds: ["early_fraud_warning:ch_1", "dp_1", "operator-x"], hold_causes: ["early_fraud_warning", "dispute", "operator"] };
    assert.deepEqual(lib.paymentHolds(held), ["early_fraud_warning:ch_1", "dp_1"]);
    assert.deepEqual(lib.operatorHolds(held), ["operator-x"]);
    assert.equal(lib.holdLabel(held, "early_fraud_warning:ch_1"), "early fraud warning early_fraud_warning:ch_1");
    assert.equal(lib.holdLabel(held, "dp_1"), "payment dispute dp_1");
  });

  it("sends no_fault only when the operator marks it", () => {
    assert.deepEqual(lib.actionRequest("acme", "unfreeze", "ok", "", "", { noFault: true }).body, { reason: "ok", no_fault: true });
    assert.deepEqual(lib.actionRequest("acme", "unfreeze", "ok", "", "").body, { reason: "ok" });
    assert.match(lib.actionEffect(team, "unfreeze", "", { noFault: true }), /as no fault found, so they do not restart the clean-day count/);
  });

  it("counts the reason in code points", () => {
    assert.equal(lib.actionProblem("freeze", "é".repeat(500), ""), "");
    assert.equal(lib.actionProblem("freeze", "🚀".repeat(500), ""), "");
    assert.match(lib.actionProblem("freeze", "é".repeat(501), ""), /500 characters/);
  });
});
