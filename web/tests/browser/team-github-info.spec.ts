import { expect, test } from "./fixtures";

test.use({ hasTouch: true });

test("GitHub explanations open by keyboard and touch", async ({ page }) => {
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/v1/capabilities") {
      await route.fulfill({
        json: {
          mode: "cluster",
          teams: { enabled: true },
          github_app: { slug: "sparkwing-test" },
        },
      });
    } else if (path === "/api/v1/me") {
      const team = { slug: "acme", display_name: "Acme", role: "owner" };
      await route.fulfill({
        json: {
          user: { id: "u1", email: "a@example.com", name: "A" },
          active_team: team,
          memberships: [team],
          invitations: [],
        },
      });
    } else if (path === "/api/v1/team/github-app") {
      await route.fulfill({
        json: {
          slug: "sparkwing-test",
          installations: [
            {
              installation_id: 7,
              account_id: 8,
              account_login: "acme",
              account_type: "Organization",
              suspended: false,
              connected_by: "u1",
              created_at: 1_790_000_000,
              updated_at: 1_790_000_000,
              manage_url: "https://github.com/settings/installations/7",
            },
          ],
        },
      });
    } else if (path.endsWith("/repositories")) {
      await route.fulfill({
        json: {
          repositories: [
            { repository_id: 10, full_name: "acme/app", private: true },
          ],
        },
      });
    } else if (path === "/api/v1/team/github-app/triggers") {
      await route.fulfill({ json: { triggers: [] } });
    } else if (path === "/api/v1/team/github-app/extra-repos") {
      await route.fulfill({ json: { extra_repos: [] } });
    } else {
      await route.fulfill({ json: {} });
    }
  });
  await page.goto("/team/github");

  const info = page.getByRole("button", { name: /About GitHub runs/ });
  await info.focus();
  await expect(page.getByRole("tooltip")).toContainText(
    "Pull requests from forks are not run.",
  );
  await page.getByRole("heading", { name: "Runs from GitHub" }).click();
  await expect(page.getByRole("tooltip")).toHaveCount(0);

  await info.tap();
  await expect(page.getByRole("tooltip")).toBeVisible();
  const target = await info.boundingBox();
  expect(target?.width).toBeGreaterThanOrEqual(44);
  expect(target?.height).toBeGreaterThanOrEqual(44);
  await expect(
    page.getByRole("button", { name: /About extra repositories/ }),
  ).toBeVisible();
});
