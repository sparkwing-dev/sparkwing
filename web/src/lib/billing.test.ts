import assert from "node:assert/strict";
import { afterEach, before, beforeEach, describe, it } from "node:test";

type BillingLib = typeof import("./billing");
let lib!: BillingLib;

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
    lib = await import("./billing");
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

// The contract's decided pricing: a credit is $0.001, 100,000 micro to the
// credit, and the 2/4/8 vCPU classes are priced at 90% of GitHub Actions.
const billing = {
  team: "acme",
  balance_micro: 123_000_000,
  micro_per_credit: 100_000,
  credits_per_dollar: 1_000,
  slice_seconds: 60,
  purchase_min_cents: 50,
  trusted: true,
  rate_table: [
    { cores: 8, micro_per_second: 33_000 },
    { cores: 2, micro_per_second: 9_000 },
    { cores: 4, micro_per_second: 18_000 },
  ],
  storage_rate_micro_per_gb_day: 0,
  storage_free_allowance_bytes: 0,
  checkout_enabled: true,
  can_purchase: true,
  usage: [],
  storage_charged_micro: 0,
  grants: [],
};

describe("balance", () => {
  it("converts micro to whole credits and dollars", () => {
    assert.equal(lib.balanceCredits(123_000_000, billing), 1_230);
    assert.equal(lib.balanceUSD(123_000_000, billing), 1.23);
    assert.equal(lib.fmtCredits(1_230), "1,230");
    assert.equal(lib.fmtUSD(1.23), "$1.23");
  });

  it("never rounds a balance up", () => {
    // 99,999 micro is just short of one credit and a fraction of a cent.
    assert.equal(lib.balanceCredits(99_999, billing), 0);
    assert.equal(lib.balanceCredits(1_999_999, billing), 19);
    assert.equal(lib.balanceUSD(1_999_999, billing), 0.01);
    assert.equal(lib.balanceUSD(999_999, billing), 0);
  });

  it("rounds a negative balance toward zero and keeps its sign", () => {
    assert.equal(lib.balanceCredits(-150_000, billing), -1);
    assert.equal(lib.fmtCredits(lib.balanceCredits(-99_999, billing)), "0");
    assert.equal(lib.fmtUSD(-2.5), "-$2.50");
  });

});

describe("charges and grants", () => {
  it("round to the nearest hundredth of a credit and cent", () => {
    assert.equal(lib.amountCredits(2_800_000, billing), 28);
    // Three two-core seconds, 0.27 credits, must not read as free.
    assert.equal(lib.amountCredits(27_000, billing), 0.27);
    assert.equal(lib.fmtCredits(lib.amountCredits(27_000, billing)), "0.27");
    assert.equal(lib.amountCredits(540_400, billing), 5.4);
    assert.equal(lib.amountCredits(-500_000_000, billing), -5_000);
    assert.equal(lib.amountUSD(500_000_000, billing), 5);
    assert.equal(lib.amountUSD(1_500_000, billing), 0.02);
  });

  it("names grant kinds for people", () => {
    assert.equal(lib.grantLabel("paid"), "Purchase");
    assert.equal(lib.grantLabel("free"), "Grant");
    assert.equal(lib.grantLabel("reversal"), "Refund");
    assert.equal(lib.grantLabel("promo"), "promo");
  });
});

describe("price table", () => {
  it("derives per-second, per-minute and per-hour prices from the rate table", () => {
    const rows = lib.priceRows(billing);
    assert.deepEqual(
      rows.map((r) => r.cores),
      [2, 4, 8],
    );
    assert.equal(lib.fmtRate(rows[0].creditsPerSecond), "0.09");
    assert.equal(lib.fmtRate(rows[0].creditsPerMinute), "5.4");
    assert.equal(lib.fmtRateUSD(rows[0].usdPerHour), "$0.32");
    assert.equal(lib.fmtRateUSD(rows[1].usdPerHour), "$0.65");
    assert.equal(lib.fmtRateUSD(rows[2].usdPerHour), "$1.19");
    assert.equal(lib.fmtRate(rows[2].creditsPerMinute), "19.8");
  });

  it("computes the per-vCPU-hour headline", () => {
    const price = lib.vcpuHourPrice({
      ...billing,
      rate_table: [
        { cores: 2, micro_per_second: 9_000 },
        { cores: 4, micro_per_second: 18_000 },
      ],
    });
    assert.ok(price);
    assert.equal(lib.fmtRateUSD(price.usd), "$0.16");
    assert.equal(price.uniform, true);
  });

  it("headlines the eight-core price, the cheapest vCPU on the default ladder", () => {
    const price = lib.vcpuHourPrice(billing);
    assert.ok(price);
    assert.equal(lib.fmtRateUSD(price.usd), "$0.15");
    assert.equal(price.uniform, false);
  });

  it("reports the lowest per-vCPU price when classes differ", () => {
    const price = lib.vcpuHourPrice({
      ...billing,
      rate_table: [
        { cores: 2, micro_per_second: 10_000 },
        { cores: 8, micro_per_second: 30_000 },
      ],
    });
    assert.ok(price);
    assert.equal(price.uniform, false);
    assert.equal(lib.fmtRateUSD(price.usd), "$0.14");
  });

  it("has no headline without classes", () => {
    assert.equal(lib.vcpuHourPrice({ ...billing, rate_table: [] }), null);
  });

  it("keeps fractional credit rates readable", () => {
    assert.equal(lib.fmtRate(0.5), "0.5");
    assert.equal(lib.fmtRate(1234.5), "1,234.5");
    assert.equal(lib.fmtRateUSD(0.0036), "$0.0036");
    assert.equal(lib.fmtRateUSD(0), "$0.00");
  });
});

describe("storagePrice", () => {
  it("is absent while storage is free", () => {
    assert.equal(lib.storagePrice(billing), null);
  });

  it("prices a GB-day and states the free allowance in the controller's gibibytes", () => {
    const price = lib.storagePrice({
      ...billing,
      storage_rate_micro_per_gb_day: 360_000,
      storage_free_allowance_bytes: 10 * 2 ** 30,
    });
    assert.ok(price);
    assert.equal(price.creditsPerGBDay, 3.6);
    assert.equal(lib.fmtRateUSD(price.usdPerGBDay), "$0.0036");
    assert.equal(price.freeGB, 10);
    assert.equal(lib.fmtGB(price.freeGB), "10 GB");
  });
});

describe("purchase amount", () => {
  it("parses dollars and cents", () => {
    assert.equal(lib.parseDollars("25"), 2_500);
    assert.equal(lib.parseDollars(" $25.5 "), 2_550);
    assert.equal(lib.parseDollars("25.05"), 2_505);
    assert.equal(lib.parseDollars("1,000"), 100_000);
    assert.equal(lib.parseDollars("0"), 0);
  });

  it("rejects anything that is not a plain amount", () => {
    for (const bad of ["", "abc", "-5", "5.001", "1e3", "5.", ".5", "5 5"]) {
      assert.equal(lib.parseDollars(bad), null, bad);
    }
  });

  it("states the credits an amount buys", () => {
    assert.equal(lib.creditsForCents(2_500, billing), 25_000);
    assert.equal(lib.creditsForCents(1, billing), 10);
    assert.equal(
      lib.fmtCredits(lib.creditsForCents(50_000, billing)),
      "500,000",
    );
  });


});

describe("checkoutReturn", () => {
  it("reads only the two return states", () => {
    assert.equal(lib.checkoutReturn("success"), "success");
    assert.equal(lib.checkoutReturn("cancelled"), "cancelled");
    assert.equal(lib.checkoutReturn("paid"), null);
    assert.equal(lib.checkoutReturn(null), null);
  });
});

describe("getBilling", () => {
  it("reads the active team's billing and fills missing lists", async () => {
    respond = () =>
      Response.json({ ...billing, usage: null, grants: undefined });
    const got = await lib.getBilling();
    assert.equal(calls.length, 1);
    assert.equal(calls[0].url, "/api/v1/team/billing");
    assert.equal(calls[0].method, "GET");
    assert.deepEqual(got.usage, []);
    assert.deepEqual(got.grants, []);
    assert.equal(got.can_purchase, true);
    assert.equal(got.frozen, false);
  });

  it("reads a held team as frozen", async () => {
    respond = () => Response.json({ ...billing, frozen: true });
    assert.equal((await lib.getBilling()).frozen, true);
  });

  it("carries the controller's refusal", async () => {
    respond = () => Response.json({ error: {code: "not_found", message: "team not found"} }, { status: 404 });
    await assert.rejects(lib.getBilling(), (err: Error) => {
      assert.ok(err instanceof lib.BillingApiError);
      assert.equal(err.status, 404);
      assert.equal(err.message, "Load billing: team not found");
      return true;
    });
  });
});

describe("startCheckout", () => {
  it("posts only the amount through the session and returns the link", async () => {
    respond = () =>
      Response.json({ url: "https://checkout.stripe.com/c/pay/cs_test_1" });
    const url = await lib.startCheckout(2_500);
    assert.equal(url, "https://checkout.stripe.com/c/pay/cs_test_1");
    assert.equal(calls.length, 1);
    const [call] = calls;
    assert.equal(call.url, "/api/v1/team/billing/checkout");
    assert.equal(call.method, "POST");
    assert.equal(call.headers.get("Content-Type"), "application/json");
    assert.ok(call.headers.get("Idempotency-Key"));
    assert.equal(call.headers.get("X-CSRF-Token"), "session-csrf");
    assert.deepEqual(JSON.parse(call.body), { amount_cents: 2_500 });
  });



  for (const [status, body, pattern] of [
    [
      400,
      { error: {code: "bad_request", message: "amount out of range", details: {reason: "amount_out_of_range"} } },
      /refused this amount: amount out of range/,
    ],
    [403, {error: {code: "forbidden", message:"forbidden"}}, /Only a team owner/],
    [502, "bad gateway", /Nothing was charged/],
    [
      503,
      {error: {code: "unavailable", message: "no service", details: {reason: "checkout_unavailable"}}},
      /not available on this controller/,
    ],
    [500, "", /Checkout failed \(500\)/],
  ] as const) {
    it(`explains a ${status}`, async () => {
      respond = () =>
        new Response(typeof body === "string" ? body : JSON.stringify(body), {
          status,
        });
      await assert.rejects(lib.startCheckout(2_500), (err: Error) => {
        assert.ok(err instanceof lib.BillingApiError);
        assert.equal(err.status, status);
        assert.match(err.message, pattern);
        return true;
      });
    });
  }

  it("refuses an answer without a link", async () => {
    respond = () => Response.json({ url: "javascript:alert(1)" });
    await assert.rejects(lib.startCheckout(2_500), /checkout link/);
  });
});

it("decision 112 permits prepaid funds above the retired dollar caps", () => {
 assert.equal(lib.purchaseProblem(600_000,billing),null);
});

it("prepay accepts the provider minimum and refuses smaller amounts", () => {
 assert.equal(lib.purchaseProblem(50,billing),null);
 assert.equal(lib.purchaseProblem(49,billing),"Minimum purchase $0.50.");
});

describe("standing requests", () => {
  const standing = {
    tier: "Trusted",
    history: { tier: "Established", paid_micro: 0 },
    prepay_ceiling: "Trusted",
    can_request: true,
  };

  it("explains the one-tier prepay ceiling", () => {
    assert.match(lib.prepayNote(standing), /raise standing to Trusted\. High needs \$500 paid/);
    assert.match(
      lib.prepayNote({ ...standing, history: { ...standing.history, tier: "Trusted" }, prepay_ceiling: "Trusted" }),
      /cannot raise it further\. High needs \$500 paid and 30 clean days, or an operator grant/,
    );
    assert.equal(lib.prepayNote({ ...standing, tier: "High" }), "");
  });

  it("needs a message within the limit", () => {
    assert.match(lib.standingRequestProblem("  "), /Tell us/);
    assert.match(lib.standingRequestProblem("x".repeat(1001)), /1000 characters/);
    assert.equal(lib.standingRequestProblem("é".repeat(1000)), "");
    assert.equal(lib.standingRequestProblem("🚀".repeat(1000)), "");
    assert.match(lib.standingRequestProblem("🚀".repeat(1001)), /1000 characters/);
    assert.equal(lib.standingRequestProblem("Release builds need 256 vCPU."), "");
  });

  it("describes each outcome", () => {
    const r = { id: "standing-1", team: "acme", tier: "High", message: "m", requested_at: 1, state: "open" };
    assert.match(lib.standingOutcome(r), /waiting for an operator/);
    assert.match(lib.standingOutcome({ ...r, state: "approved" }), /granted High standing/);
    assert.equal(
      lib.standingOutcome({ ...r, state: "declined", decision_reason: "Pay for 30 days first." }),
      "An operator declined the request for High standing: Pay for 30 days first.",
    );
  });

  it("posts a High request with the session's CSRF token and surfaces a refusal", async () => {
    respond = () => Response.json({ id: "standing-1", team: "acme", tier: "High", message: "m", requested_at: 1, state: "open" }, { status: 201 });
    const made = await lib.requestHighStanding("  m  ");
    assert.equal(made.state, "open");
    assert.equal(calls[0].url, "/api/v1/team/billing/standing-request");
    assert.equal(calls[0].method, "POST");
    assert.equal(calls[0].headers.get("X-CSRF-Token"), "session-csrf");
    assert.deepEqual(JSON.parse(calls[0].body), { tier: "high", message: "m" });
    respond = () => Response.json({ error: { code: "conflict", message: "the team already has an open standing request" } }, { status: 409 });
    await assert.rejects(lib.requestHighStanding("m"), /already has an open standing request/);
  });
});

describe("hold notices", () => {
  it("names each blocker and its remedy", () => {
    assert.match(lib.holdNotice("dispute"), /payment dispute is open\. Contact support/);
    assert.match(lib.holdNotice("early_fraud_warning"), /possible fraud/);
    assert.match(lib.holdNotice("operator"), /support placed a hold/);
    assert.match(lib.holdNotice("revoked"), /revoked/);
    for (const cause of ["dispute", "early_fraud_warning", "operator", "revoked"])
      assert.match(lib.holdNotice(cause), /Cloud work is stopped|Cloud work\. Contact|so Cloud work is stopped/);
  });
});

describe("automatic refill", () => {
  const refill = {
    enabled: true,
    threshold_cents: 1_000,
    amount_cents: 5_000,
    week_limit_cents: 0,
    month_limit_cents: 0,
    min_amount_cents: 1_000,
    max_amount_cents: 500_000,
    caps: { amount_cents: 5_000, per_day: 1, week_cents: 10_000 },
    open: false,
    day_count: 0,
    week_cents: 0,
    month_cents: 0,
  };

  it("checks the form the way the controller does", () => {
    const ok = { enabled: true, threshold: 1_000, amount: 5_000, week: 0, month: 0 };
    assert.equal(lib.refillProblem(ok, refill), null);
    assert.match(lib.refillProblem({ ...ok, amount: 999 }, refill) ?? "", /at least \$10/);
    assert.match(lib.refillProblem({ ...ok, amount: 500_001 }, refill) ?? "", /at most \$5,000/);
    assert.match(lib.refillProblem({ ...ok, threshold: null }, refill) ?? "", /balance that starts a refill/);
    assert.match(lib.refillProblem({ ...ok, week: null }, refill) ?? "", /0 for none/);
    assert.equal(lib.refillProblem({ ...ok, enabled: false, amount: 0 }, refill), null);
    assert.match(lib.refillProblem({ ...ok, enabled: false, amount: 500 }, refill) ?? "", /at least/);
  });

  it("names the limit that stops the next refill", () => {
    assert.equal(lib.refillStatus(refill), "");
    assert.equal(lib.refillStatus({ ...refill, enabled: false, limit: "team_weekly" }), "");
    assert.match(lib.refillStatus({ ...refill, limit: "standing_size" }), /\$50 per-refill cap/);
    assert.match(lib.refillStatus({ ...refill, limit: "standing_daily" }), /1 refill a day/);
    assert.match(lib.refillStatus({ ...refill, limit: "standing_weekly" }), /\$100 weekly cap/);
    assert.match(lib.refillStatus({ ...refill, week_limit_cents: 2_500, limit: "team_weekly" }), /weekly limit of \$25/);
    assert.match(lib.refillStatus({ ...refill, month_limit_cents: 10_000, limit: "team_monthly" }), /monthly limit of \$100.*Cloud work stops/);
  });

  it("names card saving and Add funds as recovery from a failed refill", () => {
    assert.match(lib.refillStatus({ ...refill, failed_at: 1, failure: "card_declined" }), /failed \(card_declined\)\. Save a card or add funds/);
  });

  it("defaults a response without refill to off", async () => {
    respond = () => Response.json(billing);
    const b = await lib.getBilling();
    assert.equal(b.refill.enabled, false);
    assert.equal(b.refill.min_amount_cents, 1_000);
    assert.equal(b.refill.caps.per_day, 0);
  });

  it("puts the settings and explains a missing card", async () => {
    const settings = { enabled: true, threshold_cents: 1_000, amount_cents: 5_000, week_limit_cents: 2_500, month_limit_cents: 0 };
    await lib.setRefill(settings);
    assert.equal(calls[0].url, "/api/v1/team/billing/refill");
    assert.equal(calls[0].method, "PUT");
    assert.equal(calls[0].headers.get("X-CSRF-Token"), "session-csrf");
    assert.deepEqual(JSON.parse(calls[0].body), settings);
    respond = () =>
      Response.json(
        { error: { code: "conflict", message: "card billing: save a card", details: { reason: "no_card" } } },
        { status: 409 },
      );
    await assert.rejects(lib.setRefill(settings), (err: Error) => {
      assert.ok(err instanceof lib.BillingApiError);
      assert.equal(err.code, "no_card");
      assert.match(err.message, /Save a card before turning on automatic refill/);
      return true;
    });
  });
});
