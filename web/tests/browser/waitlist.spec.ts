import { expect, test } from "./fixtures";

test("a waitlisted operator can navigate the console while customer pages stay gated", async ({ page }) => {
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const json = path === "/api/v1/capabilities"
      ? { mode: "multi-team", teams: { enabled: true } }
      : path === "/api/v1/me"
        ? {
            user: { id: "operator-wait", email: "operator@example.com", name: "Operator" },
            waitlisted: true, operator_console: true, memberships: [], invitations: [],
          }
        : path === "/api/v1/operator/waitlist"
          ? { accounts: [], total: 0 }
          : { teams: [] };
    await route.fulfill({ json });
  });
  await page.goto("/operator");
  await expect(page.getByRole("heading", { name: "Operator console" })).toBeVisible();
  const nav = page.getByRole("navigation", { name: "Operator console" });
  await nav.getByRole("link", { name: "Waitlist (0)" }).click();
  await expect(page).toHaveURL(/\/operator\?view=waitlist$/);
  await expect(page.getByRole("heading", { name: "You're on the list" })).toHaveCount(0);
  await page.goto("/runs");
  await expect(page.getByRole("heading", { name: "You're on the list" })).toBeVisible();
  await page.getByRole("link", { name: "Operator", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Operator console" })).toBeVisible();
  for (const name of ["Home", "Runs", "Fleet", "Crons", "Team"]) {
    await expect(page.getByRole("link", { name, exact: true })).toHaveCount(0);
  }
});

test("a waitlisted account sees a centered notice and no team navigation", async ({
  page,
}) => {
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/v1/capabilities") {
      await route.fulfill({
        json: { mode: "multi-team", teams: { enabled: true } },
      });
    } else if (path === "/api/v1/me") {
      await route.fulfill({
        json: {
          user: { id: "user-wait", email: "wait@example.com", name: "Wait" },
          waitlisted: true,
          memberships: [],
          invitations: [],
        },
      });
    } else {
      await route.fulfill({ json: {} });
    }
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/runs");
  const heading = page.getByRole("heading", { name: "You're on the list" });
  await expect(heading).toBeVisible();
  for (const name of ["Home", "Runs", "Fleet", "Crons"]) {
    await expect(page.getByRole("link", { name, exact: true })).toHaveCount(0);
  }
  await expect(
    page.getByRole("button", { name: "Help", exact: true }),
  ).toBeVisible();
  const card = await heading.locator("..").boundingBox();
  expect(card).not.toBeNull();
  const center = card!.x + card!.width / 2;
  expect(Math.abs(center - 1440 / 2)).toBeLessThan(40);
});
