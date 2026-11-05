import AxeBuilder from "@axe-core/playwright";
import type { Page } from "@playwright/test";
import { expect, test } from "./fixtures";
import { onboardingFixture } from "./onboarding-fixture";
import { startAuthenticatedDashboard } from "./authenticated-server";

async function selectRepository(page: Page, name = "acme/app") {
  await page.getByRole("combobox", { name: "Repository to set up", exact: true }).selectOption(name);
  await expect(page.getByText("release-frontend-ci", { exact: true })).toBeVisible();
}

async function confirmGitHub(page: Page) {
  await page.getByLabel("My enabled workflow starts a Sparkwing runner on pushes for team korey-gam", { exact: true }).check();
}

async function accessible(page: Page) {
  expect((await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21aa"]).analyze()).violations).toEqual([]);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
}

for (const width of [1440, 390]) {
  test(`owner enables Actions with one explicit action and confirms an existing workflow at ${width}px`, async ({ page, context }) => {
    await page.setViewportSize({ width, height: 900 });
    const fixture = await onboardingFixture(page);
    await page.goto("/team/github");
    await selectRepository(page);
    const activeTab = await page.getByRole("link", { name: "GitHub", exact: true }).boundingBox();
    expect(activeTab?.x).toBeGreaterThanOrEqual(0);
    expect((activeTab?.x ?? 0) + (activeTab?.width ?? 0)).toBeLessThanOrEqual(width);
    await expect(page.getByLabel("Team slug", { exact: true })).toHaveText("korey-gam");
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    await page.getByRole("button", { name: "Copy Team slug", exact: true }).click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("korey-gam");
    expect(fixture.mutations).toEqual([]);
    await expect(page.getByText("Run location: not selected", { exact: true })).toBeVisible();
    const enable = page.getByRole("button", { name: "Enable runs on GitHub Actions", exact: true });
    expect((await enable.boundingBox())?.height).toBeGreaterThanOrEqual(44);
    await accessible(page);
    await enable.click();
    await expect(page.getByText("Automatic runs: on", { exact: true })).toBeVisible();
    await expect(page.getByText("GitHub Actions access: allowed", { exact: true })).toBeVisible();
    await expect(page.getByText("Run location: GitHub Actions", { exact: true })).toBeVisible();
    expect(fixture.mutations).toEqual(["POST /api/v1/runners/github", "PUT /api/v1/team/github-app/automation"]);
    expect(fixture.permissionRequests).toEqual([
      { method: "POST", path: "/api/v1/runners/github", body: { repository: "acme/app" } },
      { method: "PUT", path: "/api/v1/team/github-app/automation", body: { repository: "acme/app", runner: "github-actions" } },
    ]);
    await expect(page.getByRole("heading", { name: "Ready for your next push" })).toHaveCount(0);
    await confirmGitHub(page);
    await expect(page.getByRole("status").filter({ hasText: "Ready for your next push" })).toContainText("release-frontend-ci on pushes to main will run on GitHub Actions");
    await expect(page.getByText("GitHub settings confirmed by you for this visit.", { exact: false })).toBeVisible();
    await expect(page.getByRole("button", { name: "Copy Starter workflow", exact: true })).toBeHidden();
    await accessible(page);
    await page.getByText("Need a workflow? Copy this starter", { exact: true }).click();
    await expect(page.getByLabel("Starter workflow", { exact: true })).toContainText("--team korey-gam");
    await expect(page.getByRole("link", { name: "Download workflow" })).toHaveAttribute("download", "sparkwing.yaml");
    await page.getByText("Other run locations and separate permissions", { exact: true }).click();
    await expect(page.getByRole("button", { name: "Revoke GitHub Actions permission" })).toBeVisible();
    await accessible(page);
    await page.getByRole("button", { name: "Revoke GitHub Actions permission" }).click();
    await expect(page.getByRole("heading", { name: "Ready for your next push" })).toHaveCount(0);
    await expect(page.getByText("GitHub Actions access: not allowed", { exact: true })).toBeVisible();
    await selectRepository(page, "acme/other");
    await expect(page.getByRole("heading", { name: "Ready for your next push" })).toHaveCount(0);
  });
}

test("new team goes directly from local provider sign-in to guided repository setup", async ({ page }) => {
  const dashboard = await startAuthenticatedDashboard({ githubApp: true });
  try {
    await page.goto(`${dashboard.origin}/login?next=/team/new`);
    await page.getByRole("link", { name: "Sign in with GitHub", exact: true }).click();
    await expect(page).toHaveURL(`${dashboard.origin}/team/new`);
    const fixture = await onboardingFixture(page, { connected: false });
    await page.reload();
    await page.getByRole("textbox", { name: "Name", exact: true }).fill("Korey's space");
    await page.getByRole("textbox", { name: /Slug/ }).fill("korey-gam");
    await page.getByRole("button", { name: "Create team", exact: true }).click();
    await expect(page).toHaveURL(`${dashboard.origin}/team/github`);
    await expect(page.getByRole("heading", { name: "1. Connect GitHub" })).toBeVisible();
    await expect(page.getByText("GitHub will return you here", { exact: false })).toBeVisible();
    await page.route("**/github/app/connect", async (route) => {
      expect(route.request().method()).toBe("POST");
      expect(route.request().postData()).toContain("csrf_token=");
      fixture.connect();
      await route.fulfill({ status: 303, headers: { location: "/team/github?connected=acme" }, body: "" });
    });
    await page.getByRole("button", { name: "Connect GitHub", exact: true }).click();
    await selectRepository(page);
    await page.getByRole("button", { name: "Enable runs on GitHub Actions", exact: true }).click();
    await confirmGitHub(page);
    await expect(page.getByRole("heading", { name: "Ready for your next push" })).toBeVisible();
  } finally { await dashboard.close(); }
});

test("members can inspect setup but cannot grant either permission", async ({ page }) => {
  const fixture = await onboardingFixture(page, { role: "member" });
  await page.goto("/team/github");
  await selectRepository(page);
  await expect(page.getByText("Ask a team owner", { exact: false })).toBeVisible();
  await expect(page.getByRole("button", { name: "Enable runs on GitHub Actions" })).toHaveCount(0);
  await page.getByText("Other run locations and separate permissions", { exact: true }).click();
  await expect(page.getByRole("combobox", { name: "Runs on", exact: true })).toBeDisabled();
  expect(fixture.mutations).toEqual([]);
});

test("saved permissions need only workflow confirmation and never grant again on reload", async ({ page }) => {
  const fixture = await onboardingFixture(page, { enabled: true, bound: true });
  await page.goto("/team/github");
  await selectRepository(page);
  await expect(page.getByRole("button", { name: "Enable runs on GitHub Actions" })).toHaveCount(0);
  await confirmGitHub(page);
  await expect(page.getByRole("heading", { name: "Ready for your next push" })).toBeVisible();
  await page.reload();
  await selectRepository(page);
  await expect(page.getByLabel("My enabled workflow starts a Sparkwing runner on pushes for team korey-gam", { exact: true })).not.toBeChecked();
  await expect(page.getByText("GitHub Actions access: allowed", { exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Ready for your next push" })).toHaveCount(0);
  expect(fixture.mutations).toEqual([]);
});

test("a failed permission grant stays incomplete and does not enable automatic runs", async ({ page }) => {
  const fixture = await onboardingFixture(page, { failBinding: true });
  await page.goto("/team/github");
  await selectRepository(page);
  await page.getByRole("button", { name: "Enable runs on GitHub Actions" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "Setup could not finish" })).toContainText("Retry to finish");
  expect(fixture.mutations).toEqual(["POST /api/v1/runners/github"]);
  await expect(page.getByText("Automatic runs: off", { exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Ready for your next push" })).toHaveCount(0);
});

for (const status of ["missing", "invalid", "unsupported", "unavailable"]) {
  test(`${status} repository configuration cannot enable Actions`, async ({ page }) => {
    const fixture = await onboardingFixture(page, { status });
    await page.goto("/team/github");
    await selectRepository(page);
    await expect(page.getByRole("button", { name: "Enable runs on GitHub Actions" })).toBeDisabled();
    await expect(page.getByText(/Commit a valid pipeline configuration/)).toBeVisible();
    expect(fixture.mutations).toEqual([]);
  });
}

test("manual overrides prevent a ready claim", async ({ page }) => {
  await onboardingFixture(page, { enabled: true, bound: true, manualOverride: true });
  await page.goto("/team/github");
  await selectRepository(page);
  await confirmGitHub(page);
  await expect(page.getByText("A manual subscription overrides these triggers.", { exact: false })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Ready for your next push" })).toHaveCount(0);
});

test("retry finishes a partial save without granting Actions access twice", async ({ page }) => {
  const fixture = await onboardingFixture(page, { failAutomationOnce: true });
  await page.goto("/team/github");
  await selectRepository(page);
  await page.getByRole("button", { name: "Enable runs on GitHub Actions" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "Setup could not finish" })).toBeVisible();
  await expect(page.getByText("Automatic runs: off", { exact: true })).toBeVisible();
  await expect(page.getByText("GitHub Actions access: allowed", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Enable runs on GitHub Actions" }).click();
  await expect(page.getByText("Automatic runs: on", { exact: true })).toBeVisible();
  expect(fixture.mutations).toEqual(["POST /api/v1/runners/github", "PUT /api/v1/team/github-app/automation", "PUT /api/v1/team/github-app/automation"]);
  await confirmGitHub(page);
  await expect(page.getByRole("heading", { name: "Ready for your next push" })).toBeVisible();
  await selectRepository(page, "acme/other");
  await selectRepository(page);
  await expect(page.getByLabel("My enabled workflow starts a Sparkwing runner on pushes for team korey-gam", { exact: true })).not.toBeChecked();
  await expect(page.getByRole("heading", { name: "Ready for your next push" })).toHaveCount(0);
});

test("pull-request-only configuration points to another run location", async ({ page }) => {
  const fixture = await onboardingFixture(page, { push: false });
  await page.goto("/team/github");
  await selectRepository(page);
  await expect(page.getByRole("button", { name: "Enable runs on GitHub Actions" })).toBeDisabled();
  await expect(page.getByText(/No push triggers found/)).toBeVisible();
  expect(fixture.mutations).toEqual([]);
});

test("CLI access leads with browser sign-in and the exact team slug", async ({ page }) => {
  const fixture = await onboardingFixture(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/team/machines");
  await expect(page.getByLabel("CLI sign-in command", { exact: true })).toContainText("--team korey-gam --profile korey-gam");
  await expect(page.getByText("sparkwing runs list --profile korey-gam", { exact: true })).toBeVisible();
  await expect(page.getByText("To renew a different profile", { exact: false })).toBeVisible();
  await expect(page.getByText("This also replaces an expired CLI token.", { exact: false })).toBeVisible();
  await page.getByRole("link", { name: "Enter a code from your terminal" }).click();
  await page.getByRole("textbox", { name: "Device code" }).fill("BCDF-GHJK");
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Approve sign-in for Korey's desktop?", exact: true })).toBeVisible();
  expect(fixture.mutations).toEqual(["POST /api/v1/auth/device/lookup"]);
  await accessible(page);
});
