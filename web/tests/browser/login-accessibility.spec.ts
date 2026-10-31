import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { startAuthenticatedDashboard } from "./authenticated-server";

test("the server-rendered login has accessible contrast and landmarks", async ({ page }) => {
  const dashboard = await startAuthenticatedDashboard({ githubApp: true });
  try {
    await page.goto(`${dashboard.origin}/login?refused=unknown_grid_reason`);
    await expect(page.getByText("or use an account access token", { exact: true })).toBeVisible();
    const accessibility = await new AxeBuilder({ page }).analyze();
    expect(accessibility.violations).toEqual([]);
    await expect(page.getByRole("main")).toHaveCount(1);
    await expect(page.getByRole("alert")).toContainText("Sign-in failed");
    await expect(page.getByRole("link", { name: "Get help" })).toHaveAttribute(
      "href", "mailto:support@sparkwing.dev",
    );
    await page.getByText("or use an account access token", { exact: true }).click();
    await expect(page.getByLabel("Account access token")).toBeVisible();
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  } finally {
    await dashboard.close();
  }
});
