import { type Page } from "@playwright/test";
import { expect, test } from "./fixtures";

const inventory = {
  secrets: 4, git_credentials: 2, run_environments: 6, run_arguments: 8,
  plaintext_run_arguments: 3, settings: 1, run_event_subscriptions: 2,
  previous_key_only: 5, unreadable: null,
};
const sealed = { ...inventory, plaintext_run_arguments: 0, previous_key_only: 0 };
const marker = "PRIVATE-KEY-OR-ARGUMENT-VALUE";
const evidence = process.env.SPARKWING_RESEAL_SCREENSHOTS;

type Options = {
  operator?: boolean;
  unreadable?: boolean;
  failure?: number;
  remaining?: boolean;
};
async function mockReseal(page: Page, options: Options = {}) {
  const calls: { method: string; verify: boolean; csrf: string | undefined; body: string | null }[] = [];
  await page.addInitScript(() => {
    Object.assign(window, { __SPARKWING_REQUIRE_LOGIN__: "true" });
    document.cookie = "__Host-sw_csrf=fake-proof; path=/; Secure";
  });
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.pathname === "/api/v1/capabilities") {
      await route.fulfill({ json: { mode: "cloud", teams: { enabled: true } } });
    } else if (url.pathname === "/api/v1/me") {
      await route.fulfill({ json: {
        user: { id: "account", email: "operator@example.test", name: "Operator" },
        operator_console: options.operator !== false,
        active_team: { slug: "acme", display_name: "Acme", role: "owner" },
        memberships: [], invitations: [],
      } });
    } else if (url.pathname === "/api/v1/operator/waitlist") {
      await route.fulfill({ json: { accounts: [], total: 0 } });
    } else if (url.pathname === "/api/v1/operator/secrets/rotate") {
      calls.push({ method: request.method(), verify: url.search === "?verify=1", csrf: request.headers()["x-csrf-token"], body: request.postData() });
      if (options.failure === calls.length) {
        await route.fulfill({ status: 403, json: { error: { code: "forbidden", message: marker, details: { reason: "reauth_required" } } } });
      } else {
        await route.fulfill({ json: {
          ...(calls.length === 3 && !options.remaining ? sealed : inventory),
          unreadable: options.unreadable ? [{ name: marker, purpose: marker, team: marker }] : null,
        } });
      }
    } else {
      await route.fulfill({ json: {} });
    }
  });
  return calls;
}

async function capture(page: Page, name: string) {
  if (evidence) await page.screenshot({ path: `${evidence}/${name}.png`, fullPage: true });
}

for (const width of [1440, 390]) {
  test(`operator verifies, confirms, re-encrypts and verifies again at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 });
    const calls = await mockReseal(page);
    await page.goto("/operator");
    await page.getByRole("navigation", { name: "Operator console" }).getByRole("link", { name: "Re-encrypt secrets" }).click();
    const verify = page.getByRole("button", { name: "Verify secrets", exact: true });
    const rotate = page.getByRole("button", { name: "Re-encrypt now", exact: true });
    const confirmation = page.getByRole("textbox", { name: "Type RE-ENCRYPT to confirm" });
    await expect(rotate).toBeDisabled();
    await expect(confirmation).toBeDisabled();
    const positions = [await verify.boundingBox(), await rotate.boundingBox()];
    await capture(page, `initial-${width}`);
    await verify.focus();
    await page.keyboard.press("Enter");
    await expect(page.getByText("Verification passed. Review the counts, then type the confirmation.")).toBeVisible();
    await expect(rotate).toBeDisabled();
    await page.keyboard.press("Tab");
    await expect(confirmation).toBeFocused();
    await confirmation.fill("re-encrypt");
    await expect(rotate).toBeDisabled();
    await confirmation.fill("RE-ENCRYPT");
    await expect(rotate).toBeEnabled();
    expect(calls).toHaveLength(1);
    await expect(page.getByRole("row", { name: "Previous key only 5 — —" })).toBeVisible();
    await capture(page, `verified-${width}`);
    expect([await verify.boundingBox(), await rotate.boundingBox()]).toEqual(positions);
    await page.keyboard.press("Tab");
    await expect(rotate).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(page.getByText("Re-encryption complete. Verification found no previous-key-only rows, plaintext argument documents or unreadable rows.")).toBeVisible();
    expect(calls).toEqual([
      { method: "POST", verify: true, csrf: "fake-proof", body: null },
      { method: "POST", verify: false, csrf: "fake-proof", body: null },
      { method: "POST", verify: true, csrf: "fake-proof", body: null },
    ]);
    await expect(page.getByRole("row", { name: "Previous key only 5 5 0" })).toBeVisible();
    await expect(page.getByRole("row", { name: "Plaintext argument documents 3 3 0" })).toBeVisible();
    await expect(rotate).toBeDisabled();
    expect([await verify.boundingBox(), await rotate.boundingBox()]).toEqual(positions);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await capture(page, `complete-${width}`);
    if (width === 390) {
      await page.getByRole("row", { name: "Unreadable rows 0 0 0" }).scrollIntoViewIfNeeded();
      await capture(page, "complete-counts-390");
    }
  });
}

for (const failure of [1, 2, 3]) {
  test(`stale sign-in at request ${failure} stops the flow and asks for sign-in`, async ({ page }) => {
    const calls = await mockReseal(page, { failure });
    await page.goto("/operator?view=secrets");
    await page.getByRole("button", { name: "Verify secrets", exact: true }).click();
    if (failure !== 1) {
      await page.getByRole("textbox", { name: "Type RE-ENCRYPT to confirm" }).fill("RE-ENCRYPT");
      await page.getByRole("button", { name: "Re-encrypt now", exact: true }).click();
    }
    await expect(page.getByRole("main").getByRole("alert")).toContainText("Sign out, sign in again, then retry within 10 minutes.");
    await expect(page.getByRole("button", { name: "Re-encrypt now", exact: true })).toBeDisabled();
    expect(calls).toHaveLength(failure);
    await expect(page.locator("body")).not.toContainText(marker);
    if (failure === 1) await capture(page, "stale-session-desktop");
  });
}

test("unreadable inventory blocks re-encryption and hides row details", async ({ page }) => {
  const calls = await mockReseal(page, { unreadable: true });
  await page.goto("/operator?view=secrets");
  await page.getByRole("button", { name: "Verify secrets", exact: true }).click();
  await expect(page.getByRole("main").getByRole("alert")).toContainText("Unreadable rows found");
  await expect(page.getByRole("row", { name: "Unreadable rows 1 — —" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Re-encrypt now", exact: true })).toBeDisabled();
  await expect(page.locator("body")).not.toContainText(marker);
  expect(calls).toHaveLength(1);
});

test("remaining rows after re-encryption are not reported as complete", async ({ page }) => {
  await mockReseal(page, { remaining: true });
  await page.goto("/operator?view=secrets");
  await page.getByRole("button", { name: "Verify secrets", exact: true }).click();
  await page.getByRole("textbox", { name: "Type RE-ENCRYPT to confirm" }).fill("RE-ENCRYPT");
  await page.getByRole("button", { name: "Re-encrypt now", exact: true }).click();
  await expect(page.getByRole("main").getByRole("alert")).toContainText("Verification still found rows needing attention");
  await expect(page.getByRole("button", { name: "Re-encrypt now", exact: true })).toBeDisabled();
});

test("a team owner without operator.console cannot see the action even by direct URL", async ({ page }) => {
  const calls = await mockReseal(page, { operator: false });
  await page.goto("/operator?view=secrets");
  await expect(page.getByText("This action requires an operator console session.")).toBeVisible();
  await expect(page.getByRole("link", { name: "Re-encrypt secrets" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Verify secrets", exact: true })).toHaveCount(0);
  expect(calls).toEqual([]);
});


test("pending requests lock the controls and a failed recheck consumes the earlier verification", async ({ page }) => {
  const calls = await mockReseal(page);
  let release: () => void = () => {};
  const held = new Promise<void>((resolve) => { release = resolve; });
  let entered: () => void = () => {};
  const started = new Promise<void>((resolve) => { entered = resolve; });
  await page.route("**/api/v1/operator/secrets/rotate?verify=1", async (route) => {
    entered();
    await held;
    await route.fallback();
  });
  await page.goto("/operator?view=secrets");
  const verify = page.getByRole("button", { name: "Verify secrets", exact: true });
  const rotate = page.getByRole("button", { name: "Re-encrypt now", exact: true });
  await verify.click();
  await started;
  await expect(verify).toBeDisabled();
  await expect(rotate).toBeDisabled();
  release();
  const confirmation = page.getByRole("textbox", { name: "Type RE-ENCRYPT to confirm" });
  await confirmation.fill("RE-ENCRYPT");
  await expect(rotate).toBeEnabled();
  await page.route("**/api/v1/operator/secrets/rotate?verify=1", (route) => route.fulfill({ json: { ...inventory, unreadable: "invalid" } }));
  await verify.click();
  await expect(page.getByRole("main").getByRole("alert")).toContainText("Incomplete verification counts");
  await expect(rotate).toBeDisabled();
  await expect(confirmation).toHaveValue("");
  expect(calls).toHaveLength(1);
});
