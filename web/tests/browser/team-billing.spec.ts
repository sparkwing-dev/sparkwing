import { type Page } from "@playwright/test";
import { expect, test } from "./fixtures";

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
    { cores: 2, micro_per_second: 10_000 },
    { cores: 4, micro_per_second: 20_000 },
    { cores: 8, micro_per_second: 40_000 },
  ],
  storage_rate_micro_per_gb_day: 0,
  storage_free_allowance_bytes: 0,
  checkout_enabled: true,
  can_purchase: true,
  usage: [
    {
      run_id: "run-abc",
      seconds: 140,
      amount_micro: 2_800_000,
      last_charged_at: 1_790_000_000,
    },
  ],
  storage_charged_micro: 0,
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
  const seen = { billingReads: 0, checkoutBodies: [] as string[] };
  const role = mock.role ?? "owner";
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/v1/capabilities") {
      await route.fulfill({
        json: { mode: "cluster", teams: { enabled: true }, billing: { enabled: true } },
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

  await expect(page.getByText("24,600 credits", { exact: true })).toBeVisible();
  await expect(
    page.getByText(/\$1\.23 · a team balance holds up to \$5,000/),
  ).toBeVisible();
  await expect(
    page.getByText("$0.18 per vCPU-hour", { exact: false }),
  ).toBeVisible();
  const twoCore = page.getByRole("row", { name: /2 vCPU/ });
  await expect(twoCore).toContainText("120");
  await expect(twoCore).toContainText("$0.36");
  await expect(page.getByRole("link", { name: "run-abc" })).toHaveAttribute(
    "href",
    "/runs?run=run-abc",
  );
  await expect(page.getByRole("row", { name: /Purchase/ })).toContainText(
    "$5.00",
  );
  await expect(
    page.getByText("Purchases are final. Credits never expire.", {
      exact: false,
    }),
  ).toBeVisible();

  const amount = page.getByLabel("Amount in US dollars");
  const buy = page.getByRole("button", { name: "Buy credits" });
  await amount.fill("501");
  await expect(
    page.getByText("Enter an amount from $5 to $500."),
  ).toBeVisible();
  await expect(buy).toBeDisabled();

  await amount.fill("25");
  await expect(page.getByText("Buys 500,000 credits.")).toBeVisible();
  await buy.click();
  await expect(page).toHaveURL("https://checkout.stripe.com/c/pay/cs_test_1");
  expect(seen.checkoutBodies.map((b) => JSON.parse(b))).toEqual([
    { amount_cents: 2_500 },
  ]);
});

test("a refused checkout states the cap and the balance", async ({ page }) => {
  await mockController(page, {
    checkout: {
      status: 409,
      json: {
        error: "over cap",
        code: "balance_cap",
        balance_micro: 499_500_000_000,
        cap_micro: 500_000_000_000,
        amount_micro: 1_000_000_000,
      },
    },
  });
  await page.goto("/team/billing");
  await page.getByLabel("Amount in US dollars").fill("10");
  await page.getByRole("button", { name: "Buy credits" }).click();
  await expect(page.locator("p[role=alert]")).toContainText(
    "capped at $5,000. The balance is $4,995.00 (99,900,000 credits)",
  );
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
    page.getByRole("button", { name: "Buy credits" }),
  ).toBeDisabled();
  await expect(page.getByText("No runs have been charged yet.")).toBeVisible();
  await expect(page.getByText("No purchases yet.")).toBeVisible();
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
  await expect(page.getByText("24,600 credits", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Buy credits" })).toHaveCount(
    0,
  );
});
