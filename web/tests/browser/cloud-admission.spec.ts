import { type Page } from "@playwright/test";
import { expect, test } from "./fixtures";

async function mockCloud(page: Page, unsupported: string[]) {
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/v1/capabilities") {
      await route.fulfill({ json: { mode: "multi-team", teams: { enabled: true } } });
      return;
    }
    if (path === "/api/v1/me") {
      const team = { slug: "demo", display_name: "Demo", role: "owner" };
      await route.fulfill({ json: {
        user: { id: "user-demo", email: "demo@example.com", name: "Demo" },
        active_team: team, memberships: [team], invitations: [],
      } });
      return;
    }
    if (path === "/api/v1/approvals/pending") {
      await route.fulfill({ json: { approvals: [] } });
      return;
    }
    if (path === "/api/v1/queue" || path.startsWith("/api/v1/capacity/profiles")) {
      unsupported.push(path);
      await route.fulfill({ status: path === "/api/v1/queue" ? 404 : 501, json: {} });
      return;
    }
    await route.fulfill({ json: {} });
  });
}

test("Cloud routes explain local admission without polling unsupported APIs", async ({ page }) => {
  const unsupported: string[] = [];
  await mockCloud(page, unsupported);

  await page.goto("/queue");
  await expect(page.getByRole("heading", { name: "Queue is local to a machine" })).toBeVisible();
  await expect(page.getByRole("paragraph").getByRole("link", { name: "Fleet" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Queue", exact: true })).toHaveCount(0);
  expect(unsupported).toEqual([]);

  await page.goto("/capacity");
  await expect(page.getByRole("heading", { name: "Capacity is local to a machine" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Capacity", exact: true })).toHaveCount(0);
  expect(unsupported).toEqual([]);
});
