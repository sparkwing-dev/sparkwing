import { type Page } from "@playwright/test";
import { expect, test } from "./fixtures";

const billing = {
  team: "acme",
  balance_micro: 123_000_000,
  micro_per_credit: 100_000,
  credits_per_dollar: 1_000,
  slice_seconds: 60,
  purchase_min_cents: 50,
  purchase_max_cents: 100_000,
  trusted: true,
  rate_table: [
    { cores: 2, micro_per_second: 9_000 },
    { cores: 4, micro_per_second: 18_000 },
    { cores: 8, micro_per_second: 33_000 },
  ],
  storage_rate_micro_per_gb_day: 0,
  storage_free_allowance_bytes: 0,
  checkout_enabled: true,
  can_purchase: true,
  usage: [
    {
      run_id: "run-abc",
      seconds: 3,
      amount_micro: 27_000,
      last_charged_at: 1_790_000_000,
    },
  ],
  storage_charged_micro: 0,
  can_add_card: false,
  refill: {
    enabled: false,
    threshold_cents: 0,
    amount_cents: 0,
    week_limit_cents: 0,
    month_limit_cents: 0,
    min_amount_cents: 1_000,
    max_amount_cents: 100_000,
    caps: { amount_cents: 20_000, per_day: 2, week_cents: 50_000 },
    open: false,
    day_count: 0,
    week_cents: 0,
    month_cents: 0,
  },
  limits: { standing: "Established", cloud_vcpu: 8, max_job_seconds: 14400 },
  standing: {
    tier: "Established",
    history: { tier: "Established", paid_micro: 500_000_000, first_paid_at: 1_790_000_000 },
    prepay_ceiling: "Trusted",
    can_request: true,
  },
  grants: [
    {
      id: "grant-1",
      kind: "paid",
      amount_micro: 500_000_000,
      reference: "pi_123",
      created_at: 1_790_000_000,
    },
  ],
};

type Mock = {
  billing?: Record<string, unknown>;
  role?: "owner" | "editor" | "reader";
  checkout?: { status: number; json: Record<string, unknown> };
  refill?: { status: number; json?: Record<string, unknown> };
};

async function mockController(page: Page, mock: Mock = {}) {
  const seen = { billingReads: 0, checkoutBodies: [] as string[], standingBodies: [] as string[], refillBodies: [] as string[] };
  const role = mock.role ?? "owner";
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/v1/capabilities") {
      await route.fulfill({
        json: {
          mode: "cluster",
          teams: { enabled: true },
          billing: { enabled: true },
        },
      });
    } else if (path === "/api/v1/me") {
      const team = { slug: "acme", display_name: "Acme", role };
      await route.fulfill({
        json: {
          user: { id: "u1", email: "a@example.com", name: "A" },
          active_team: team,
          memberships: [team],
          invitations: [],
        },
      });
    } else if (path === "/api/v1/team/billing") {
      seen.billingReads++;
      await route.fulfill({ json: { ...billing, ...mock.billing } });
    } else if (path === "/api/v1/team/billing/standing-request") {
      seen.standingBodies.push(request.postData() ?? "");
      await route.fulfill({
        status: 201,
        json: { id: "standing-1", team: "acme", tier: "High", message: "m", requested_at: 1_790_000_000, state: "open" },
      });
    } else if (path === "/api/v1/team/billing/refill") {
      seen.refillBodies.push(request.postData() ?? "");
      await route.fulfill(mock.refill ?? { status: 204 });
    } else if (path === "/api/v1/team/billing/checkout") {
      seen.checkoutBodies.push(request.postData() ?? "");
      await route.fulfill(
        mock.checkout ?? {
          status: 200,
          json: { url: "https://checkout.stripe.com/c/pay/cs_test_1" },
        },
      );
    } else {
      await route.fulfill({ json: {} });
    }
  });
  await page.route("https://checkout.stripe.com/**", (route) =>
    route.fulfill({ contentType: "text/html", body: "<h1>Stripe</h1>" }),
  );
  return seen;
}

test("an owner sees prices from the controller and starts a checkout", async ({
  page,
}) => {
  const seen = await mockController(page);
  await page.goto("/team/billing");

  await expect(page.getByText("1,230 credits", { exact: true })).toBeVisible();
  await expect(page.getByText(/\$1\.23/)).toBeVisible();
  const twoCore = page.getByRole("row", { name: /2 vCPU/ });
  await expect(
    twoCore.getByRole("cell", { name: "0.09", exact: true }),
  ).toBeVisible();
  await expect(twoCore).toContainText("$0.32");
  await expect(
    page.getByRole("columnheader", { name: "Credits / minute" }),
  ).toHaveCount(0);
  await expect(page.getByRole("row", { name: /run-abc/ })).toContainText(
    "0.27",
  );
  await expect(page.getByRole("heading", { name: "History" })).toBeVisible();
  await expect(page.getByRole("link", { name: "run-abc" })).toHaveAttribute(
    "href",
    "/runs?run=run-abc",
  );
  await expect(page.getByRole("row", { name: /Purchase/ })).toContainText(
    "$5.00",
  );
  await expect(
    page.getByText("Purchases are final. Credits never expire.", {
      exact: true,
    }),
  ).toBeVisible();
  const amount = page.getByLabel("Amount in US dollars");
  const buy = page.getByRole("button", { name: "Add funds" });
  await expect(page.getByText("Minimum purchase $0.50. Cloud usage draws on the balance.")).toBeVisible();
  await amount.fill("0.49");
  await expect(page.getByText("Minimum purchase $0.50.",{exact:true})).toBeVisible();
  await expect(buy).toBeDisabled();
  await amount.fill("1000");
  await expect(buy).toBeEnabled();
  await amount.fill("1000.01");
  await expect(buy).toBeDisabled();
  await expect(page.getByText("Each payment is at most $1,000. Contact us through the billing request form for larger payments.")).toBeVisible();

  await amount.fill("25");
  await expect(page.getByText("Buys 25,000 credits.")).toBeVisible();
  await buy.click();
  await expect(page).toHaveURL("https://checkout.stripe.com/c/pay/cs_test_1");
  expect(seen.checkoutBodies.map((b) => JSON.parse(b))).toEqual([
    { amount_cents: 2_500 },
  ]);
});


test("a reader cannot buy and a returning payment reloads the balance", async ({
  page,
}) => {
  const seen = await mockController(page, {
    role: "reader",
    billing: { can_purchase: false, usage: [], grants: [] },
  });
  await page.goto("/team/billing?checkout=success");
  await expect(
    page.getByText(
      "Payment received. Credits appear once Stripe confirms the payment.",
    ),
  ).toBeVisible();
  await expect(
    page.getByText("Only a team owner can buy credits."),
  ).toBeVisible();
  await expect(page.getByLabel("Amount in US dollars")).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "Add funds" }),
  ).toBeDisabled();
  await expect(page.getByText("No runs have been charged yet.")).toBeVisible();
  await expect(page.getByText("No history yet.")).toBeVisible();
  await expect
    .poll(() => seen.billingReads, { timeout: 8_000 })
    .toBeGreaterThan(2);
});

test("checkout stays hidden when the controller has no checkout service", async ({
  page,
}) => {
  await mockController(page, { billing: { checkout_enabled: false } });
  await page.goto("/team/billing?checkout=cancelled");
  await expect(
    page.getByText("Checkout was cancelled. Nothing was charged."),
  ).toBeVisible();
  await expect(page.getByText("1,230 credits", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Add funds" })).toHaveCount(
    0,
  );
});

test("a held team sees the hold and no pay-now or budget controls", async ({
  page,
}) => {
  await mockController(page, {
    billing: {
      balance_micro: -15_000_000_000,
      frozen: true,
      hold_cause: "dispute",
      can_add_card: true,
      card: { added_at: 1_790_000_000 },
    },
  });
  await page.goto("/team/billing");
  await expect(page.getByRole("alert").filter({ hasText: "Cloud work is stopped while a payment dispute is open" })).toBeVisible();
  await expect(page.getByText(/balance is empty/)).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Manage card" })).toBeVisible();
  await expect(page.getByRole("button", { name: /Pay .* now/ })).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Spend limits" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Add funds" })).toBeVisible();
});

test("an owner turns on automatic refill within the limits", async ({ page }) => {
  const seen = await mockController(page, {
    billing: {
      can_add_card: true,
      card: { added_at: 1_790_000_000 },
    },
  });
  await page.goto("/team/billing");
  await expect(page.getByRole("heading", { name: "Automatic refill" })).toBeVisible();
  await expect(page.getByText(/Established standing allows at most \$200 per refill, 2 refills a day and \$500 a week/)).toBeVisible();
  const toggle = page.getByLabel("Refill automatically");
  await expect(toggle).not.toBeChecked();
  await expect(page.getByLabel("When the balance is below")).toHaveValue("10");
  await expect(page.getByLabel("Add this amount")).toHaveValue("50");
  await toggle.check();
  const amount = page.getByLabel("Add this amount");
  const save = page.getByRole("button", { name: "Save" });
  await amount.fill("9");
  await expect(page.getByText("A refill is at least $10.")).toBeVisible();
  await expect(save).toBeDisabled();
  await amount.fill("25");
  await page.getByLabel("Weekly limit").fill("100");
  await save.click();
  await expect.poll(() => seen.refillBodies.map((b) => JSON.parse(b))).toEqual([
    { enabled: true, threshold_cents: 1_000, amount_cents: 2_500, week_limit_cents: 10_000, month_limit_cents: 0 },
  ]);
  await expect(page.getByText("Saved.")).toBeVisible();
});

test("refill needs a saved card and explains a stopped refill", async ({ page }) => {
  await mockController(page, {
    billing: {
      can_add_card: true,
      refill: {
        ...billing.refill,
        enabled: true,
        threshold_cents: 1_000,
        amount_cents: 5_000,
        week_limit_cents: 2_500,
        limit: "team_weekly",
      },
    },
  });
  await page.goto("/team/billing");
  await expect(page.getByText(/Another refill would pass the team's weekly limit of \$25\./)).toBeVisible();
  await expect(page.getByText("Save a card to turn on automatic refill.")).toBeVisible();
  await expect(page.getByText("Save a card before turning on automatic refill.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Save" })).toBeDisabled();
});

test("a failed refill tells the owner to update the card", async ({ page }) => {
  await mockController(page, {
    billing: {
      can_add_card: true,
      card: { added_at: 1_790_000_000 },
      refill: { ...billing.refill, enabled: true, threshold_cents: 1_000, amount_cents: 5_000, failed_at: 1_790_000_000, failure: "card_declined" },
    },
  });
  await page.goto("/team/billing");
  await expect(page.getByText(/The last refill failed \(card_declined\)\. Update your card with Manage card/)).toBeVisible();
});

test("an owner contacts the operators for higher limits or larger payments", async ({ page }) => {
  const seen = await mockController(page);
  await page.goto("/team/billing");
  await expect(
    page.getByText(/Prepaid funds can raise standing to Trusted\. High needs \$500 paid/),
  ).toBeVisible();
  const message = page.getByLabel("Contact us for higher tiers, higher limits or larger payments");
  await page.getByRole("button", { name: "Send request" }).click();
  await expect(page.getByText(/Tell us about the higher limits or larger payments/)).toBeVisible();
  await message.fill("Release builds need 256 vCPU.");
  await page.getByRole("button", { name: "Send request" }).click();
  await expect.poll(() => seen.standingBodies.map((b) => JSON.parse(b))).toEqual([
    { tier: "high", message: "Release builds need 256 vCPU." },
  ]);
});

test("a declined request shows the operator's reason", async ({ page }) => {
  await mockController(page, {
    billing: {
      standing: {
        ...billing.standing,
        can_request: true,
        request: { id: "standing-1", team: "acme", tier: "High", message: "m", requested_at: 1_790_000_000, state: "declined", decided_at: 1_790_100_000, decision_reason: "Pay for 30 days first." },
      },
    },
  });
  await page.goto("/team/billing");
  await expect(
    page.getByText("An operator declined the request for High standing: Pay for 30 days first."),
  ).toBeVisible();
});

test("a High-standing owner requests larger payments", async ({ page }) => {
  const seen = await mockController(page, {
    billing: { standing: { ...billing.standing, tier: "High", can_request: true } },
  });
  await page.goto("/team/billing");
  await page.getByLabel("Contact us for higher tiers, higher limits or larger payments").fill("We need a $2,000 payment for our release builds.");
  await page.getByRole("button", { name: "Send request" }).click();
  await expect.poll(() => seen.standingBodies.map((b) => JSON.parse(b))).toEqual([
    { tier: "high", message: "We need a $2,000 payment for our release builds." },
  ]);
});
