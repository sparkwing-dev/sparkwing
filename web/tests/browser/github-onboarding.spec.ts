import { expect, test } from "./fixtures";
import AxeBuilder from "@axe-core/playwright";

test("repository onboarding requires separate trigger and Actions consent", async ({ page }) => {
  let enabled = false;
  let bound = false;
  const mutations: string[] = [];
  const workflow = "name: sparkwing\n# --team actual-slug\n";
  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    const path = url.pathname;
    if (method !== "GET") mutations.push(`${method} ${path}`);
    if (path === "/api/v1/capabilities") {
      await route.fulfill({ json: { mode: "cluster", teams: { enabled: true }, github_app: { slug: "test-app" } } });
    } else if (path === "/api/v1/me") {
      const team = { slug: "actual-slug", display_name: "Different Display Name", role: "owner" };
      await route.fulfill({ json: { user: { id: "u1", email: "owner@example.com", name: "Owner" }, active_team: team, memberships: [team], invitations: [] } });
    } else if (path === "/api/v1/team/github-app") {
      await route.fulfill({ json: { slug: "test-app", installations: [{ installation_id: 7, account_id: 8, account_login: "acme", account_type: "Organization", suspended: false, manage_url: "https://github.com/settings/installations/7" }] } });
    } else if (path.endsWith("/repositories")) {
      await route.fulfill({ json: { repositories: [{ repository_id: 9, full_name: "acme/app", private: true }] } });
    } else if (path === "/api/v1/team/github-app/automation") {
      if (method === "PUT") { expect(route.request().postDataJSON()).toEqual({ repository: "acme/app" }); enabled = true; }
      await route.fulfill({ json: { repository: "acme/app", repository_id: 9, installation_id: 7, enabled, default_branch: "main", source_sha: "abc123", status: "ready", pipelines: [{ pipeline: "gate", push: true, branches: ["main"], pull_request: false, actions: [], base_branches: [], manual_override: true }] } });
    } else if (path === "/api/v1/team/github-runners") {
      if (method === "POST") { expect(route.request().postDataJSON()).toEqual({ repository: "acme/app" }); bound = true; }
      await route.fulfill({ json: { bindings: bound ? [{ repository: "acme/app", repository_id: 9, repository_owner_id: 8 }] : [], workflow } });
    } else {
      await route.fulfill({ json: { triggers: [], extra_repos: [] } });
    }
  });
  await page.goto("/team/github");
  await page.getByRole("combobox", { name: "Repository to set up", exact: true }).selectOption("acme/app");
  await expect(page.getByText("Push branches: main", { exact: true })).toBeVisible();
  await expect(page.getByText(/Manual subscription controls this pipeline/)).toBeVisible();
  expect(mutations).toEqual([]);
  await page.getByRole("button", { name: "Enable repository declarations", exact: true }).click();
  await expect(page.getByText("Repository automation: enabled", { exact: true })).toBeVisible();
  await expect(page.getByText(/GitHub Actions permission: not granted/)).toBeVisible();
  await page.getByRole("button", { name: "Allow GitHub Actions execution", exact: true }).click();
  await expect(page.getByText(/GitHub Actions permission: granted/)).toBeVisible();
  await expect(page.getByText(/Workflow installation is not verified here/)).toBeVisible();
  await page.getByText("View generated workflow", { exact: true }).click();
  await expect(page.locator("pre")).toContainText("--team actual-slug");
  await expect(page.getByRole("link", { name: "Download workflow" })).toHaveAttribute("download", "sparkwing.yaml");
  expect(mutations).toEqual(["PUT /api/v1/team/github-app/automation", "POST /api/v1/team/github-runners"]);
  await page.setViewportSize({ width: 375, height: 812 });
  const allow = page.getByRole("button", { name: "Revoke GitHub Actions permission", exact: true });
  const target = await allow.boundingBox();
  expect(target?.height).toBeGreaterThanOrEqual(44);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  const accessibility = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21aa"]).analyze();
  expect(accessibility.violations).toEqual([]);
});
