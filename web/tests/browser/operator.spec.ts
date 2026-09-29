import { type Page } from "@playwright/test";
import { expect, test } from "./fixtures";

const acme = {
  team: "acme",
  display_name: "Acme",
  owners: ["olga@example.com"],
  balance_micro: 1_230_000_000,
  billing: {
    team: "acme",
    trust: "revoked",
    trusted: false,
    trust_by: "korey@example.com",
    trust_at: 1_790_000_000,
    trust_reason: "chargeback",
    purchase_limit_cents: 5_000,
    purchased_30d_cents: 2_500,
  },
  frozen: false,
  holds: [],
  events: [
    {
      at: 1_790_000_000,
      kind: "billing.trust_changed",
      actor: "korey@example.com",
      attrs: { reason: "chargeback" },
    },
  ],
};

async function mockOperator(page: Page) {
  const posts: { path: string; body: unknown }[] = [];
  let reads = 0;
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/v1/operator/teams") {
      await route.fulfill({
        json: {
          teams: [
            {
              team: "acme",
              display_name: "Acme",
              owners: ["olga@example.com"],
            },
          ],
        },
      });
    } else if (path === "/api/v1/operator/teams/acme") {
      reads++;
      await route.fulfill({ json: acme });
    } else if (path === "/api/v1/operator/waitlist") {
      await route.fulfill({ json: { accounts: [], total: 0 } });
    } else if (path.startsWith("/api/v1/operator/teams/acme/")) {
      posts.push({ path, body: JSON.parse(request.postData() ?? "{}") });
      await route.fulfill({ status: 201, json: {} });
    } else {
      await route.fulfill({ json: {} });
    }
  });
  return { posts, reads: () => reads };
}

test("the operator finds a team and reads its standing", async ({ page }) => {
  await mockOperator(page);
  await page.goto("/operator");
  await page.getByLabel("Search teams").fill("olga@");
  await page.getByRole("button", { name: "Search" }).click();
  await page.getByRole("link", { name: "Acme" }).click();
  await expect(page).toHaveURL(/\/operator\?team=acme$/);
  await expect(page.getByText("$12.30", { exact: true })).toBeVisible();
  await expect(page.getByText("$25 of $50")).toBeVisible();
  await expect(
    page.getByText(/by korey@example\.com .*: chargeback/),
  ).toBeVisible();
  await expect(page.getByText("billing.trust_changed")).toBeVisible();
});

test("an action is sent only after its confirmation names the team and effect", async ({
  page,
}) => {
  const seen = await mockOperator(page);
  await page.goto("/operator?team=acme");
  await page.getByLabel("Action").selectOption({ label: "Grant credits" });
  await expect(
    page.getByLabel("Action").locator("option", { hasText: "Set limit override" }),
  ).toHaveCount(0);
  await page.getByLabel("Amount in US dollars").fill("20");
  const review = page.getByRole("button", { name: "Review" });
  await expect(review).toBeDisabled();
  await expect(
    page.getByText("Give a reason; it is recorded with the action."),
  ).toBeVisible();
  await page.getByLabel("Reason").fill("launch promo");

  await review.click();
  const dialog = page.getByRole("alertdialog", { name: "Confirm action" });
  await expect(dialog).toContainText("Grant credits on Acme (acme)");
  await expect(dialog).toContainText("Acme receives $20 of free credit.");
  await expect(dialog).toContainText("Reason: launch promo");
  await expect(page.getByLabel("Reason")).toBeDisabled();
  await dialog.getByRole("button", { name: "Cancel" }).click();
  expect(seen.posts).toEqual([]);

  await review.click();
  const readsBefore = seen.reads();
  await dialog.getByRole("button", { name: "Confirm" }).click();
  await expect(dialog).toBeHidden();
  expect(seen.posts).toHaveLength(1);
  expect(seen.posts[0].path).toBe("/api/v1/operator/teams/acme/grants");
  expect(seen.posts[0].body).toMatchObject({
    amount_cents: 2_000,
    reason: "launch promo",
  });
  expect((seen.posts[0].body as { key: string }).key).toMatch(/.{8,}/);
  await expect.poll(seen.reads).toBeGreaterThan(readsBefore);
});

function waiting(n: number) {
  return Array.from({ length: n }, (_, i) => ({
    id: `acct-${i}`,
    email: `person${i}@example.com`,
    name: `Person ${i}`,
    provider: i % 2 ? "github" : "google",
    reason: "deployment",
    created_at: 1_790_000_000 - i * 60,
    waitlisted_at: 1_790_000_000 - i * 60,
  }));
}

async function mockWaitlist(page: Page, total: number) {
  let list = waiting(total);
  const approvals: string[][] = [];
  const offsets: string[] = [];
  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/v1/operator/waitlist") {
      const limit = Number(url.searchParams.get("limit"));
      const offset = Number(url.searchParams.get("offset"));
      if (limit > 1) offsets.push(String(offset));
      await route.fulfill({
        json: { accounts: list.slice(offset, offset + limit), total: list.length },
      });
    } else if (url.pathname === "/api/v1/operator/waitlist/approve") {
      const ids = (JSON.parse(route.request().postData() ?? "{}") as { account_ids: string[] }).account_ids;
      approvals.push(ids);
      const approved = list.filter((a) => ids.includes(a.id));
      list = list.filter((a) => !ids.includes(a.id));
      await route.fulfill({
        json: { approved: approved.map((a) => ({ id: a.id, email: a.email, active_team: a.id })) },
      });
    } else {
      await route.fulfill({ json: {} });
    }
  });
  return { approvals, offsets };
}

test("the operator approves waitlisted people one at a time or selected", async ({
  page,
}) => {
  const seen = await mockWaitlist(page, 3);
  await page.goto("/operator");
  const nav = page.getByRole("navigation", { name: "Operator console" });
  await nav.getByRole("link", { name: "Waitlist (3)" }).click();
  await expect(page).toHaveURL(/\/operator\?view=waitlist$/);

  const rows = page.locator("tbody tr");
  await expect(rows).toHaveCount(3);
  await expect(rows.nth(0)).toContainText("person0@example.com");
  await expect(rows.nth(0)).toContainText("Person 0");
  await expect(rows.nth(1)).toContainText("github");
  const selectedButton = page.getByRole("button", { name: /Approve selected/ });
  await expect(selectedButton).toBeDisabled();
  expect(seen.approvals).toEqual([]);

  await page.getByRole("button", { name: "Approve person1@example.com" }).click();
  await expect(rows).toHaveCount(2);
  await expect(nav.getByRole("link", { name: "Waitlist (2)" })).toBeVisible();
  expect(seen.approvals).toEqual([["acct-1"]]);

  await page.getByLabel("Select every account on this page").check();
  await expect(page.getByRole("button", { name: "Approve selected (2)" })).toBeEnabled();
  await page.getByLabel("Select person2@example.com").uncheck();
  await page.getByRole("button", { name: "Approve selected (1)" }).click();
  await expect(rows).toHaveCount(1);
  await expect(rows.nth(0)).toContainText("person2@example.com");
  expect(seen.approvals).toEqual([["acct-1"], ["acct-0"]]);
});

test("a long waitlist pages fifty at a time", async ({ page }) => {
  const seen = await mockWaitlist(page, 120);
  await page.goto("/operator?view=waitlist");
  await expect(page.getByText("1–50 of 120")).toBeVisible();
  await expect(page.getByRole("button", { name: "Newer" })).toBeDisabled();
  await page.getByRole("button", { name: "Older" }).click();
  await page.getByRole("button", { name: "Older" }).click();
  await expect(page.getByText("101–120 of 120")).toBeVisible();
  await expect(page.locator("tbody tr")).toHaveCount(20);
  await expect(page.getByRole("button", { name: "Older" })).toBeDisabled();
  expect(seen.offsets).toEqual(["0", "50", "100"]);
});
