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
    purchase_limit_cents: 5_000,
    purchased_30d_cents: 0,
  },
  frozen: true,
  holds: ["dp_1", "operator-x"],
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

  it("holds an amount to $0.01 through $5,000", () => {
    for (const amount of ["", "0", "abc", "5000.01"]) {
      assert.match(
        lib.actionProblem("grant-credits", "promo", amount),
        /from \$0\.01 to \$5,000/,
        amount,
      );
    }
    assert.equal(lib.actionProblem("set-limit", "vip", "$5,000"), "");
  });

  it("names the team and the effect", () => {
    assert.equal(
      lib.actionEffect(team, "grant-credits", "20"),
      "Acme receives $20 of free credit.",
    );
    assert.match(
      lib.actionEffect(team, "set-limit", "2500"),
      /Acme .* \$2,500 every 30 days/,
    );
    assert.match(
      lib.actionEffect(team, "unfreeze", ""),
      /Every hold on Acme is released \(2 now\)/,
    );
    assert.match(lib.actionEffect(team, "freeze", ""), /loses automatic trust/);
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
    assert.deepEqual(req("set-limit", "2,500").body, {
      trust: "granted",
      reason: "why",
      limit_cents: 250_000,
    });
    assert.deepEqual(req("clear-limit").body, {
      trust: "granted",
      reason: "why",
      limit_cents: 0,
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
          error: "forbidden",
          message:
            "the operator console needs the operator's own signed-in account",
        },
        { status: 403 },
      );
    await assert.rejects(
      lib.performAction(lib.actionRequest("acme", "freeze", "abuse", "", "")),
      /operator's own signed-in account/,
    );
  });
});
