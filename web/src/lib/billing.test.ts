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

// The contract's decided pricing: 5,000 micro per credit, 20,000 credits per
// dollar, 2/4/8 vCPU classes at one credit per vCPU-second.
const billing = {
  team: "acme",
  balance_micro: 123_000_000,
  balance_cap_micro: 500_000_000_000,
  micro_per_credit: 5_000,
  credits_per_dollar: 20_000,
  min_billable_seconds: 20,
  purchase_min_cents: 500,
  purchase_max_cents: 50_000,
  rate_table: [
    { cores: 8, micro_per_second: 40_000 },
    { cores: 2, micro_per_second: 10_000 },
    { cores: 4, micro_per_second: 20_000 },
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
    assert.equal(lib.balanceCredits(123_000_000, billing), 24_600);
    assert.equal(lib.balanceUSD(123_000_000, billing), 1.23);
    assert.equal(lib.fmtCredits(24_600), "24,600");
    assert.equal(lib.fmtUSD(1.23), "$1.23");
  });

  it("never rounds a balance up", () => {
    // 4,999 micro is just short of one credit and a fraction of a cent.
    assert.equal(lib.balanceCredits(4_999, billing), 0);
    assert.equal(lib.balanceCredits(1_999_999, billing), 399);
    assert.equal(lib.balanceUSD(1_999_999, billing), 0.01);
    assert.equal(lib.balanceUSD(999_999, billing), 0);
  });

  it("rounds a negative balance toward zero and keeps its sign", () => {
    assert.equal(lib.balanceCredits(-7_500, billing), -1);
    assert.equal(lib.fmtCredits(lib.balanceCredits(-4_999, billing)), "0");
    assert.equal(lib.fmtUSD(-2.5), "-$2.50");
  });

  it("formats the cap as whole dollars", () => {
    const cap = lib.balanceUSD(billing.balance_cap_micro, billing);
    assert.equal(cap, 5_000);
    assert.equal(lib.fmtUSDShort(cap), "$5,000");
    assert.equal(lib.fmtUSDShort(12.5), "$12.50");
    assert.equal(
      lib.fmtCredits(lib.balanceCredits(billing.balance_cap_micro, billing)),
      "100,000,000",
    );
  });
});

describe("charges and grants", () => {
  it("round to the nearest credit and cent", () => {
    assert.equal(lib.amountCredits(2_800_000, billing), 560);
    assert.equal(lib.amountCredits(7_500, billing), 2);
    assert.equal(lib.amountCredits(-500_000_000, billing), -100_000);
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
    assert.equal(rows[0].creditsPerSecond, 2);
    assert.equal(rows[0].creditsPerMinute, 120);
    assert.equal(lib.fmtRateUSD(rows[0].usdPerHour), "$0.36");
    assert.equal(lib.fmtRateUSD(rows[1].usdPerHour), "$0.72");
    assert.equal(lib.fmtRateUSD(rows[2].usdPerHour), "$1.44");
    assert.equal(lib.fmtRate(rows[2].creditsPerMinute), "480");
  });

  it("computes the per-vCPU-hour headline", () => {
    const price = lib.vcpuHourPrice(billing);
    assert.ok(price);
    assert.equal(lib.fmtRateUSD(price.usd), "$0.18");
    assert.equal(price.uniform, true);
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
    assert.equal(price.creditsPerGBDay, 72);
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
    assert.equal(lib.creditsForCents(2_500, billing), 500_000);
    assert.equal(lib.creditsForCents(1, billing), 200);
    assert.equal(
      lib.fmtCredits(lib.creditsForCents(50_000, billing)),
      "10,000,000",
    );
  });

  it("accepts the inclusive range and refuses outside it", () => {
    assert.equal(lib.purchaseProblem(500, billing), null);
    assert.equal(lib.purchaseProblem(50_000, billing), null);
    const range = "Enter an amount from $5 to $500.";
    assert.equal(lib.purchaseProblem(499, billing), range);
    assert.equal(lib.purchaseProblem(50_001, billing), range);
    assert.equal(lib.purchaseProblem(null, billing), range);
  });

  it("refuses a purchase that would pass the balance cap", () => {
    const nearCap = { ...billing, balance_micro: 499_000_000_000 };
    const msg = lib.purchaseProblem(10_000, nearCap);
    assert.ok(msg);
    assert.match(msg, /capped at \$5,000/);
    assert.match(msg, /\$4,990\.00/);
    assert.match(msg, /99,800,000 credits/);
    assert.match(msg, /up to \$10\.00 more/);
    // Exactly reaching the cap is allowed.
    assert.equal(lib.purchaseProblem(1_000, nearCap), null);
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
  });

  it("carries the controller's refusal", async () => {
    respond = () => Response.json({ error: "team not found" }, { status: 404 });
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
    const url = await lib.startCheckout(2_500, billing);
    assert.equal(url, "https://checkout.stripe.com/c/pay/cs_test_1");
    assert.equal(calls.length, 1);
    const [call] = calls;
    assert.equal(call.url, "/api/v1/team/billing/checkout");
    assert.equal(call.method, "POST");
    assert.equal(call.headers.get("Content-Type"), "application/json");
    assert.equal(call.headers.get("X-CSRF-Token"), "session-csrf");
    assert.deepEqual(JSON.parse(call.body), { amount_cents: 2_500 });
  });

  it("states the cap and the balance on a balance_cap refusal", async () => {
    respond = () =>
      Response.json(
        {
          error: "over cap",
          code: "balance_cap",
          balance_micro: 499_500_000_000,
          cap_micro: 500_000_000_000,
          amount_micro: 1_000_000_000,
        },
        { status: 409 },
      );
    await assert.rejects(lib.startCheckout(1_000, billing), (err: Error) => {
      assert.ok(err instanceof lib.BillingApiError);
      assert.equal(err.status, 409);
      assert.equal(err.code, "balance_cap");
      assert.match(err.message, /capped at \$5,000/);
      assert.match(
        err.message,
        /balance is \$4,995\.00 \(99,900,000 credits\)/,
      );
      assert.match(err.message, /up to \$5\.00 more/);
      return true;
    });
  });

  it("counts the team's open checkouts against the room left", async () => {
    respond = () =>
      Response.json(
        {
          error: "over cap",
          code: "balance_cap",
          balance_micro: 400_000_000_000,
          open_micro: 60_000_000_000,
          cap_micro: 500_000_000_000,
          amount_micro: 50_000_000_000,
        },
        { status: 409 },
      );
    await assert.rejects(lib.startCheckout(50_000, billing), (err: Error) => {
      assert.match(err.message, /\$600\.00 in checkouts still open/);
      assert.match(err.message, /up to \$400\.00 more/);
      return true;
    });
  });

  for (const [status, body, pattern] of [
    [
      400,
      { error: "amount out of range", code: "amount_out_of_range" },
      /refused this amount: amount out of range/,
    ],
    [403, { error: "forbidden" }, /Only a team owner/],
    [502, "bad gateway", /Nothing was charged/],
    [
      503,
      { error: "no service", code: "checkout_unavailable" },
      /not available on this controller/,
    ],
    [500, "", /Checkout failed \(500\)/],
  ] as const) {
    it(`explains a ${status}`, async () => {
      respond = () =>
        new Response(typeof body === "string" ? body : JSON.stringify(body), {
          status,
        });
      await assert.rejects(lib.startCheckout(2_500, billing), (err: Error) => {
        assert.ok(err instanceof lib.BillingApiError);
        assert.equal(err.status, status);
        assert.match(err.message, pattern);
        return true;
      });
    });
  }

  it("refuses an answer without a link", async () => {
    respond = () => Response.json({ url: "javascript:alert(1)" });
    await assert.rejects(lib.startCheckout(2_500, billing), /checkout link/);
  });
});
