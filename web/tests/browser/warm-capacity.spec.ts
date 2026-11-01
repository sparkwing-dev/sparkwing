import { expect, test } from "./fixtures";

test("operator saves warm settings, sees metrics and recovers from a refused save", async ({ page }) => {
  let settings = { class: "small", mode: "off", manual_count: 0, auto_min: 0, auto_max: 0, start_time_target_seconds: 10 };
  let rejectSave = false;
  const writes: unknown[] = [];
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/v1/operator/warm-capacity") {
      if (request.method() === "PUT") {
        writes.push(request.postDataJSON());
        if (rejectSave) {
          await route.fulfill({ status: 400, json: { error: { message: "Capacity settings rejected" } } });
          return;
        }
        settings = request.postDataJSON();
        await route.fulfill({ json: {} });
      } else {
        await route.fulfill({ json: {
          classes: [{ ...settings, warm: 2, desired: settings.mode === "manual" ? settings.manual_count : 0,
            start_1h: { p50_seconds: 4, p95_seconds: 12 }, start_24h: { p50_seconds: null, p95_seconds: null },
            cold_starts_24h: 3, cold_starts_7d: 9, upsizes_24h: 1, upsizes_7d: 5 }],
          new_signups_24h: 7, new_signups_7d: 17, active_teams_24h: 8, active_teams_7d: 18,
        } });
      }
    } else if (path === "/api/v1/operator/waitlist") {
      await route.fulfill({ json: { accounts: [], total: 0 } });
    } else { await route.fulfill({ json: {} }); }
  });
  await page.goto("/operator");
  await page.getByRole("link", { name: "Warm capacity" }).click();
  await expect(page.getByRole("heading", { name: "Warm capacity" })).toBeVisible();
  const form = page.getByRole("form", { name: "small warm capacity" });
  await expect(form.getByRole("combobox", { name: "Mode", exact: true })).toHaveValue("off");
  await expect(form.getByLabel("Start-time target (seconds)")).toHaveValue("10");
  await expect(page.getByText("Warm nodes: 2 · Desired nodes: 0")).toBeVisible();
  await expect(page.getByRole("cell", { name: "4.0s", exact: true })).toBeVisible();
  await expect(page.getByRole("cell", { name: "-", exact: true })).toHaveCount(2);
  await expect(page.getByText("Cold starts (24h / 7d): 3 / 9. Upsizes (24h / 7d): 1 / 5.")).toBeVisible();
  await form.getByRole("combobox", { name: "Mode", exact: true }).selectOption("manual");
  await form.getByLabel("Manual count").fill("3");
  await form.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByText("Warm nodes: 2 · Desired nodes: 3")).toBeVisible();
  expect(writes).toEqual([{ class: "small", mode: "manual", manual_count: 3, auto_min: 0, auto_max: 0, start_time_target_seconds: 10 }]);
  await form.getByRole("combobox", { name: "Mode", exact: true }).selectOption("auto");
  await form.getByLabel("Auto minimum").fill("4");
  await form.getByRole("button", { name: "Save", exact: true }).click();
  await expect(form.getByRole("alert")).toHaveText("Auto maximum must be at least the minimum.");
  expect(writes).toHaveLength(1);
  await form.getByLabel("Auto maximum").fill("6");
  rejectSave = true;
  await form.getByRole("button", { name: "Save", exact: true }).click();
  await expect(form.getByRole("alert")).toHaveText("Capacity settings rejected");
  await expect(form.getByRole("button", { name: "Save", exact: true })).toBeEnabled();
  rejectSave = false;
  await form.getByRole("button", { name: "Save", exact: true }).click();
  await expect(form.getByRole("alert")).toHaveCount(0);
  await expect(form.getByRole("combobox", { name: "Mode", exact: true })).toHaveValue("auto");
});
