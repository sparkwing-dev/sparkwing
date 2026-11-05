import { expect, test } from "./fixtures";
import { onboardingFixture } from "./onboarding-fixture";

test("an approved account creates its first team from Home", async ({ page }) => {
  const fixture = await onboardingFixture(page, { noTeam: true, connected: false });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Create your first team" })).toBeVisible();
  await expect(page.getByText("Permission denied", { exact: false })).toHaveCount(0);
  expect(fixture.requestsWithoutTeam).toEqual([]);
  await page.getByRole("link", { name: "Create your first team", exact: true }).click();
  await expect(page).toHaveURL(/\/team\/new$/);
  await page.getByRole("textbox", { name: "Name", exact: true }).fill("First Team");
  await expect(page.getByRole("textbox", { name: /Slug/ })).toHaveValue("first-team");
  await page.getByRole("button", { name: "Create team", exact: true }).click();
  await expect(page).toHaveURL(/\/team\/github$/);
  await expect(page.getByRole("heading", { name: "1. Connect GitHub" })).toBeVisible();
  await expect(page.getByRole("button", { name: "First Team owner" })).toBeVisible();
  expect(fixture.mutations).toEqual(["POST /api/v1/teams"]);
  expect(fixture.requestsWithoutTeam).toEqual([]);
});

test("team pages prompt for a first team without sending team requests", async ({ page }) => {
  const fixture = await onboardingFixture(page, { noTeam: true });
  for (const path of ["/runs", "/team", "/team/secrets", "/cluster", "/crons", "/queue", "/capacity", "/analytics", "/device"]) {
    await page.goto(path);
    await expect(page.getByRole("heading", { name: "Create your first team" })).toBeVisible();
    await expect(page.getByText("Permission denied", { exact: false })).toHaveCount(0);
  }
  await page.goto("/team/new");
  await expect(page.getByRole("heading", { name: "Create a team", exact: true })).toBeVisible();
  expect(fixture.requestsWithoutTeam).toEqual([]);
  expect(fixture.mutations).toEqual([]);
});

test("an approved session can select an existing team without creating another", async ({ page }) => {
  const fixture = await onboardingFixture(page, { noActiveTeam: true });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Choose a team" })).toBeVisible();
  await page.getByRole("button", { name: "Choose a team", exact: true }).click();
  await page.getByRole("menuitemradio", { name: /Korey's space/ }).click();
  await expect(page.getByRole("button", { name: "Korey's space owner" })).toBeVisible();
  expect(fixture.mutations).toEqual(["POST /api/v1/me/active-team"]);
  expect(fixture.requestsWithoutTeam).toEqual([]);
});
