import { expect, test } from "@playwright/test";
import { startAuthenticatedDashboard } from "./authenticated-server";

test("GitHub repository access update returns to the signed-in GitHub tab", async ({ page }) => {
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
    await page.route("https://github.example/**", async (route) => {
      await route.fulfill({ contentType: "text/html", body: `<a href="${dashboard.origin}/github/app/setup?installation_id=42&setup_action=update">Return to Sparkwing</a>` });
    });
    await page.goto("https://github.example/settings");
    const setupRequest = page.waitForRequest((request) => new URL(request.url()).pathname === "/github/app/setup");
    await page.getByRole("link", { name: "Return to Sparkwing" }).click();
    expect(await (await setupRequest).headerValue("cookie")).toBeNull();
    await expect(page).toHaveURL(/\/team\/github(?:\?access_updated=1)?$/);
    await expect(page.getByText("Repository access updated on GitHub")).toBeVisible();
  } finally {
    await dashboard.close();
  }
});
