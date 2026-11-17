import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { expect, test } from "./fixtures";
import { onboardingFixture } from "./onboarding-fixture";

test("account deletion previews teams, requires confirmation and signs out", async ({ page }, testInfo) => {
  await onboardingFixture(page);
  await page.route("**/api/v1/me/team-deletions", route => route.fulfill({ json: { deletions: [], truncated: false, next_cursor: "" } }));
  await page.route("**/api/v1/me/deletion", route => route.fulfill({ json: { account_id: "u1", teams: ["personal-space", "project-work"], blocked_teams: [] } }));
  let removed = false;
  let releaseDeletion = () => {};
  const release = new Promise<void>(resolve => { releaseDeletion = resolve; });
  await page.route("**/api/v1/me", async route => {
    if (route.request().method() !== "DELETE") return route.fallback();
    expect(route.request().postDataJSON()).toEqual({ confirm_account_id: "u1", confirm_teams: ["personal-space", "project-work"] });
    removed = true;
    await release;
    await route.fulfill({ json: { account_id: "u1", deleted_teams: ["personal-space", "project-work"] } });
  });
  await page.goto("/account");
  await page.screenshot({ path: testInfo.outputPath("ui-overview.png"), fullPage: true });
  await page.getByRole("button", { name: "Review account deletion" }).click();
  await expect(page.getByText("personal-space", { exact: true })).toBeVisible();
  await expect(page.getByText("project-work", { exact: true })).toBeVisible();
  const confirm = page.getByLabel("Type your account ID to confirm:", { exact: false });
  await expect(confirm).toBeFocused();
  const remove = page.getByRole("button", { name: "Delete account permanently" });
  await expect(remove).toBeDisabled();
  expect(removed).toBe(false);
  await remove.scrollIntoViewIfNeeded();
  await page.screenshot({ path: testInfo.outputPath("ui-confirm-wide.png"), fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await remove.scrollIntoViewIfNeeded();
  await page.screenshot({ path: testInfo.outputPath("ui-confirm-narrow.png"), fullPage: true });
  await confirm.fill("u1");
  await page.screenshot({ path: testInfo.outputPath("ui-enabled.png"), fullPage: true });
  const before = await remove.boundingBox();
  await remove.click();
  await expect(remove).toBeDisabled();
  expect(await remove.boundingBox()).toEqual(before);
  await page.screenshot({ path: testInfo.outputPath("ui-busy.png"), fullPage: true });
  releaseDeletion();
  await expect(page).toHaveURL(/\/login\?account_deleted=1$/);
  expect(removed).toBe(true);
});

test("a no-team account can reach deletion and a blocked account gets ownership guidance", async ({ page }, testInfo) => {
  await onboardingFixture(page, { noTeam: true });
  await page.route("**/api/v1/me/team-deletions", route => route.fulfill({ json: { deletions: [], truncated: false, next_cursor: "" } }));
  await page.route("**/api/v1/me/deletion", route => route.fulfill({ json: { account_id: "u1", teams: [], blocked_teams: ["shared-team"] } }));
  await page.goto("/account");
  await page.getByRole("button", { name: "Review account deletion" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "Transfer ownership" })).toContainText("Transfer ownership to another member or delete these teams first: shared-team");
  await expect(page.getByRole("button", { name: "Delete account permanently" })).toHaveCount(0);
  await page.screenshot({ path: testInfo.outputPath("ui-blocked.png"), fullPage: true });
});

test("failed deletion retains the form and requires a fresh confirmation", async ({ page }, testInfo) => {
  await onboardingFixture(page);
  await page.route("**/api/v1/me/team-deletions", route => route.fulfill({ json: { deletions: [], truncated: false, next_cursor: "" } }));
  await page.route("**/api/v1/me/deletion", route => route.fulfill({ json: { account_id: "u1", teams: [], blocked_teams: [] } }));
  await page.route("**/api/v1/me", route => route.request().method() === "DELETE" ? route.fulfill({ status: 409, json: { error: { code: "conflict", message: "teams to delete changed" } } }) : route.fallback());
  await page.goto("/account");
  const review = page.getByRole("button", { name: "Review account deletion" });
  await review.click();
  const confirm = page.getByLabel("Type your account ID to confirm:", { exact: false });
  await confirm.fill("u1");
  await page.getByRole("button", { name: "Delete account permanently" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "teams to delete changed" })).toBeFocused();
  await expect(page.getByRole("alert").filter({ hasText: "teams to delete changed" })).toContainText("teams to delete changed");
  await expect(confirm).toHaveValue("");
  await page.screenshot({ path: testInfo.outputPath("ui-error.png"), fullPage: true });
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(review).toBeFocused();
});

test("long team slugs stay inside the mobile confirmation panel", async ({ page }, testInfo) => {
  await onboardingFixture(page);
  const slug = "a".repeat(63);
  await page.route("**/api/v1/me/team-deletions", route => route.fulfill({ json: { deletions: [], truncated: false, next_cursor: "" } }));
  await page.route("**/api/v1/me/deletion", route => route.fulfill({ json: { account_id: "u1", teams: [slug], blocked_teams: [] } }));
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/account");
  await page.getByRole("button", { name: "Review account deletion" }).click();
  const team = page.getByText(slug, { exact: true });
  await expect(team).toBeVisible();
  expect(await team.evaluate(node => node.scrollWidth <= node.clientWidth)).toBe(true);
  await page.getByRole("button", { name: "Delete account permanently" }).scrollIntoViewIfNeeded();
  await page.screenshot({ path: testInfo.outputPath("ui-long-team-mobile.png"), fullPage: true });
});

test("a stale preview requests reauthentication and shows the archive notice", async ({ page }, testInfo) => {
  await onboardingFixture(page);
  const archiveNotice = readFileSync(resolve(__dirname, "../../../docs/account-deletion-retention.md"), "utf8").split("\n\n")[1];
  await page.route("**/api/v1/me/team-deletions", route => route.fulfill({ json: { deletions: [], truncated: false, next_cursor: "" } }));
  await page.route("**/api/v1/me/deletion", route => route.fulfill({ json: { account_id: "u1", teams: ["personal"], blocked_teams: [], sign_in_required: true, archive_notice: archiveNotice, credential_notice: "Runner and CI tokens you created in shared teams are revoked too." } }));
  let signedOut = false;
  await page.route("**/api/v1/auth/logout", async route => { signedOut = true; await route.fulfill({ status: 204 }); });
  await page.goto("/account");
  await page.getByRole("button", { name: "Review account deletion" }).click();
  const signIn = page.getByRole("button", { name: "Sign in again", exact: true });
  await expect(signIn).toBeFocused();
  await expect(page.getByText(archiveNotice, { exact: false })).toBeVisible();
  await expect(page.getByRole("link", { name: "Retention details" })).toHaveAttribute("href", "https://sparkwing.dev/docs/account-deletion-retention");
  await expect(page.getByRole("button", { name: "Delete account permanently" })).toHaveCount(0);
  await signIn.scrollIntoViewIfNeeded();
  await page.screenshot({ path: testInfo.outputPath("reauth-wide.png"), fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn.scrollIntoViewIfNeeded();
  await page.screenshot({ path: testInfo.outputPath("reauth-mobile.png"), fullPage: true });
  await signIn.click();
  await expect(page).toHaveURL(/\/login\?next=%2Faccount$/);
  expect(signedOut).toBe(true);
});

test("freshness expiring after preview removes the destructive form", async ({ page }) => {
  await onboardingFixture(page);
  await page.route("**/api/v1/me/team-deletions", route => route.fulfill({ json: { deletions: [], truncated: false, next_cursor: "" } }));
  await page.route("**/api/v1/me/deletion", route => route.fulfill({ json: { account_id: "u1", teams: [], blocked_teams: [], sign_in_required: false, archive_notice: "Archive notice from documentation" } }));
  await page.route("**/api/v1/me", route => route.request().method() === "DELETE" ? route.fulfill({ status: 403, json: { error: { code: "forbidden", message: "sign in again", details: { reason: "reauth_required" } } } }) : route.fallback());
  await page.goto("/account");
  await page.getByRole("button", { name: "Review account deletion" }).click();
  await page.getByLabel("Type your account ID to confirm:", { exact: false }).fill("u1");
  await page.getByRole("button", { name: "Delete account permanently" }).click();
  await expect(page.getByRole("button", { name: "Sign in again", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Delete account permanently" })).toHaveCount(0);
});
