import { expect, test } from "@playwright/test";
import { startAuthenticatedDashboard } from "./authenticated-server";

test("an existing App callback requests available once", async ({ page }) => {
  const dashboard = await startAuthenticatedDashboard({ githubApp: true });
  try {
    await page.goto(`${dashboard.origin}/login?next=/team/github`);
    await page.getByRole("link", { name: "Sign in with GitHub" }).click();
    await expect(page).toHaveURL(`${dashboard.origin}/team/github`);

    const available: string[] = [];
    const aborted: string[] = [];
    page.on("request", (request) => {
      if (new URL(request.url()).pathname === "/github/app/available") {
        available.push(request.url());
      }
    });
    page.on("requestfailed", (request) => {
      if (new URL(request.url()).pathname === "/github/app/available") {
        aborted.push(request.failure()?.errorText ?? "failed");
      }
    });
    const callback = page.waitForResponse((response) =>
      new URL(response.url()).pathname === "/github/app/callback",
    );
    await page.getByText("Already installed the app?", { exact: true }).click();
    await page.getByRole("button", { name: "Connect an existing installation", exact: true }).click();
    expect((await callback).status()).toBe(303);
    await expect(
      page.getByRole("heading", { name: "No existing installations" }),
    ).toBeVisible();
    expect(available).toEqual([`${dashboard.origin}/github/app/available`]);
    expect(aborted).toEqual([]);
  } finally {
    await dashboard.close();
  }
});

test("a fresh connect that cannot finish offers the existing installation", async ({ page }) => {
  const dashboard = await startAuthenticatedDashboard({ githubApp: true });
  try {
    await page.goto(`${dashboard.origin}/login?next=/team/github`);
    await page.getByRole("link", { name: "Sign in with GitHub" }).click();
    await expect(page).toHaveURL(`${dashboard.origin}/team/github`);

    // GitHub installs the App but returns a state this browser's flow no longer holds.
    await page.route("**/apps/sparkwing-test/installations/new**", (route) =>
      route.fulfill({
        status: 303,
        headers: {
          location: `${dashboard.origin}/github/app/setup?installation_id=7&setup_action=install&state=expired`,
        },
      }),
    );
    await page.getByRole("button", { name: "Connect GitHub", exact: true }).click();
    await expect(page.getByRole("heading", { name: "GitHub was not connected" })).toBeVisible();
    await expect(page.getByText("If the App is already installed on GitHub, connect that installation.")).toBeVisible();
    await page.getByRole("link", { name: "Connect an existing installation" }).click();
    await expect(page).toHaveURL(new RegExp(`^${dashboard.origin}/team/github(\\?connect=existing)?$`));
    const existing = page.getByRole("button", { name: "Connect an existing installation", exact: true });
    await expect(existing).toBeFocused();
    await existing.click();
    await expect(
      page.getByRole("heading", { name: "No existing installations" }),
    ).toBeVisible();
  } finally {
    await dashboard.close();
  }
});
