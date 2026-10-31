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
