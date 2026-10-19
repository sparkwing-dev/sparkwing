import { type Page } from "@playwright/test";
import { expect, test } from "./fixtures";

const billing = {
  team: "acme",
  balance_micro: 123_000_000,
  micro_per_credit: 100_000,
  credits_per_dollar: 1_000,
  min_billable_seconds: 60,
  purchase_min_cents: 50,
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
  card_billed: false,
  can_add_card: false,
  limits: {
 standing:"New",cloud_vcpu:8,max_job_seconds:14400,
    budget_cents: 50_000,
    rung_cents: 10_000,
    spent_30d_cents: 1_200,
    spent_today_cents: 300,
    headroom_cents: 12,
    binding: "balance",
  },
  standing: {
    tier: "Established",
    history: { tier: "Established", paid_micro: 500_000_000, successful_charges: 0, first_paid_at: 1_790_000_000 },
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
};

async function mockController(page: Page, mock: Mock = {}) {
  const seen = { billingReads: 0, checkoutBodies: [] as string[], standingBodies: [] as string[] };
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
  await expect(page.getByText("Minimum purchase $0.50. Usage draws prepaid funds before card billing resumes.")).toBeVisible();
  await amount.fill("0.49");
  await expect(page.getByText("Minimum purchase $0.50.",{exact:true})).toBeVisible();
  await expect(buy).toBeDisabled();
  await amount.fill("6000");
  await expect(buy).toBeEnabled();

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

test("a card-billed team with a failed charge pays it by hand and can still add funds", async ({
  page,
}) => {
  await mockController(page, {
    billing: {
      balance_micro: -15_000_000_000,
      frozen: true,
      hold_cause: "decline",
      card_billed: true,
      can_add_card: true,
      card: { brand: "visa", last4: "4242", added_at: 1_790_000_000 },
      open_charge: {
        id: "cardcharge_1",
        amount_cents: 15_000,
        failures: 1,
        decline_code: "insufficient_funds",
      },
      limits: {
        ...billing.limits,
        headroom_cents: -15_000,
        binding: "charge_failed",
      },
    },
  });
  await page.goto("/team/billing");
  await expect(page.getByText("$150.00 owed")).toBeVisible();
  await expect(page.getByRole("alert").filter({ hasText: "The card was declined. Update the card or pay the charge" })).toBeVisible();
  await expect(page.getByText(/payment dispute/)).toHaveCount(0);
  await expect(page.getByText("visa ending 4242")).toBeVisible();
  await expect(
    page.getByText(/A charge of \$150 failed \(insufficient_funds\)/),
  ).toBeVisible();
  await expect(
    page.getByText(
      "The card was declined. Update the card or pay the charge now; new Cloud work waits until then.",
    ),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Pay $150 now" }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "Add funds" })).toBeVisible();
});

test("an owner asks the operators for High standing", async ({ page }) => {
  const seen = await mockController(page);
  await page.goto("/team/billing");
  await expect(
    page.getByText(/Prepaid funds can raise standing to Trusted\. High needs \$500 paid/),
  ).toBeVisible();
  const message = page.getByLabel("Contact us to unlock higher tiers");
  await page.getByRole("button", { name: "Request High standing" }).click();
  await expect(page.getByText(/Tell us what the team runs/)).toBeVisible();
  await message.fill("Release builds need 256 vCPU.");
  await page.getByRole("button", { name: "Request High standing" }).click();
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
