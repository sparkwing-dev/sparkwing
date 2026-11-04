import { expect, test } from "./fixtures";

test("a repository access refusal explains the fix and links to Team GitHub", async ({ page }) => {
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/v1/capabilities") {
      await route.fulfill({ json: { mode: "multi-team", teams: { enabled: true } } });
    } else if (path === "/api/v1/me") {
      const team = { slug: "demo", display_name: "Demo", role: "owner" };
      await route.fulfill({ json: {
        user: { id: "user-demo", email: "demo@example.com", name: "Demo" },
        active_team: team, memberships: [team], invitations: [],
      } });
    } else if (path === "/api/v1/runs" && route.request().method() === "POST") {
      await route.fulfill({ status: 403, json: { error: {
        code: "forbidden", message: "repository is not authorized",
        details: { reason: "repository_unauthorized" },
      } } });
    } else if (path === "/api/v1/runs") {
      await route.fulfill({ json: { runs: [] } });
    } else if (path === "/api/v1/pipelines") {
      await route.fulfill({ json: { pipelines: [] } });
    } else {
      await route.fulfill({ json: {} });
    }
  });

  await page.goto("/runs");
  await page.getByRole("button", { name: "+ Start a run" }).click();
  const dialog = page.getByRole("dialog", { name: "Start a run" });
  await dialog.getByLabel("Pipeline", { exact: true }).fill("build");
  await dialog.getByLabel("Repository", { exact: true }).fill("https://github.com/acme/private");
  await dialog.getByRole("button", { name: "Run", exact: true }).click();

  const error = dialog.getByRole("alert");
  await expect(error).toContainText("Sparkwing does not have access to this repository.");
  await expect(error).toContainText("Connect or grant the repository under Team > GitHub.");
  await expect(error.getByRole("link", { name: "Open Team > GitHub" })).toHaveAttribute("href", "/team/github");
  await expect(error).not.toContainText("repository_unauthorized");
  await expect(error).not.toContainText('"error"');
});

test("server errors give a friendly remedy, support contact and request id", async ({ page }) => {
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/v1/runs") {
      await route.fulfill({ status: 500, headers: { "X-Request-Id": "req-console-500" },
        json: { error: { message: "private database failure" } } });
    } else {
      await route.fulfill({ json: {} });
    }
  });
  await page.goto("/runs");
  await expect(page.getByText(/Try again.*support@sparkwing.dev/)).toBeVisible();
  await expect(page.getByText(/Request ID: req-console-500/)).toBeVisible();
  await expect(page.locator("body")).not.toContainText("private database failure");
  await expect(page.locator("body")).not.toContainText('"error"');
});

test("the run toolbar fits a 375px viewport without scrolling", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 812 });
  await page.route("**/api/v1/**", (route) => route.fulfill({ json: {} }));
  await page.goto("/runs");
  const start = page.getByRole("button", { name: "+ Start a run" });
  await expect(start).toBeVisible();
  const bounds = await start.boundingBox();
  expect(bounds).not.toBeNull();
  expect(bounds!.x).toBeGreaterThanOrEqual(0);
  expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(375);
  expect(await page.evaluate(() => window.scrollX)).toBe(0);
});

// Rows no longer show trust (the Setup tab shows the sealed class); they must still never guess one from the branch.
test("run rows never guess a trust class from main", async ({ page }) => {
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    await route.fulfill({ json: path === "/api/v1/runs" ? { runs: [{
      id: "sealed-run", pipeline: "build", status: "success", git_branch: "main",
      trust_class: "asserted", started_at: "2026-10-31T10:00:00Z",
      finished_at: "2026-10-31T10:01:00Z",
    }] } : {} });
  });
  await page.goto("/runs");
  await expect(page.locator('[data-run-id="sealed-run"]')).toBeVisible();
  await expect(page.locator('[data-run-id="sealed-run"]')).not.toContainText("Trust:");
  await expect(page.locator("body")).not.toContainText("verified-default");
});

test("a run detail server failure shows support and its request id", async ({ page }) => {
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/v1/runs/detail-error") {
      await route.fulfill({ status: 500, headers: { "X-Request-Id": "req-detail-500" },
        json: { error: { message: "private database failure" } } });
    } else {
      await route.fulfill({ json: {} });
    }
  });
  await page.goto("/runs?run=detail-error");
  const alert = page.getByRole("alert").filter({ hasText: "req-detail-500" });
  await expect(alert).toContainText("Try again");
  await expect(alert).toContainText("support@sparkwing.dev");
  await expect(alert).toContainText("Request ID: req-detail-500");
  await expect(page.locator("body")).not.toContainText("private database failure");
  await expect(page.locator("body")).not.toContainText('"error"');
});
