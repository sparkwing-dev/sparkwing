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
