import { expect, test } from "./fixtures";

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
