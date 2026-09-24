import { expect, test } from "@playwright/test";
import { startAuthenticatedDashboard } from "./authenticated-server";

test("an owner authorizes and picks an installation without visiting install settings", async ({ page }) => {
  const dashboard = await startAuthenticatedDashboard();
  try {
  await page.goto(`${dashboard.origin}/login`);
  await page.getByLabel("Username").fill("admin");
  await page.getByLabel("Password").fill("correct-horse");
  await page.getByRole("button", { name: "Create admin and sign in" }).click();
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/v1/capabilities") {
      await route.fulfill({ json: { mode: "cluster", teams: { enabled: true }, github_app: { slug: "sparkwing-test" } } });
    } else if (path === "/api/v1/me") {
      const team = { slug: "acme", display_name: "Acme", role: "owner" };
      await route.fulfill({ json: { user: { id: "u1", email: "a@example.com", name: "A" }, active_team: team, memberships: [team], invitations: [] } });
    } else if (path === "/api/v1/team/github-app") {
      await route.fulfill({ json: { slug: "sparkwing-test", installations: [] } });
    } else if (path === "/api/v1/team/github-app/triggers") {
      await route.fulfill({ json: { triggers: [] } });
    } else {
      await route.fulfill({ json: {} });
    }
  });
  let visitedInstall = false;
  await page.route("https://github.example/**", async (route) => {
    if (route.request().url().includes("installations/new")) visitedInstall = true;
    await route.fulfill({ status: 302, headers: { location: `${dashboard.origin}/github/app/callback?code=picker-code&state=picker-state` } });
  });
  await page.goto(`${dashboard.origin}/team/github`);
  await page.getByRole("button", { name: "Already installed the app? Connect an existing installation" }).click();
  await expect(page.getByRole("heading", { name: "Connect an existing installation" })).toBeVisible();
  await expect(page.getByText("bound-org")).toBeVisible();
  await expect(page.getByText("Connected to another team")).toBeVisible();
  await expect(page.getByRole("button", { name: "Connect", exact: true })).toHaveCount(1);
  expect(visitedInstall).toBe(false);
  await page.getByRole("button", { name: "Connect", exact: true }).click();
  await expect(page).toHaveURL(/\/team\/github\?connected=octo-org/);
  } finally {
    await dashboard.close();
  }
});
