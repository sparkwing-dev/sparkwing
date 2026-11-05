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
