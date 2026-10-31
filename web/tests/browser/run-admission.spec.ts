import { type Page, type Route } from "@playwright/test";
import { expect, test } from "./fixtures";

async function admissionAPI(page: Page, submit: (route: Route) => Promise<void>, existingRun = false) {
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (route.request().method() === "POST") return submit(route);
    if (path === "/api/v1/runs") {
      return route.fulfill({ json: { runs: existingRun ? [{
        id: "unfunded", pipeline: "build", status: "failed",
        error: "run did not start: out of credits: add funds",
        created_at: "2026-10-31T10:00:00Z",
      }] : [] } });
    }
    return route.fulfill({ json: {} });
  });
}

async function openForm(page: Page) {
  await page.goto("/runs");
  await page.getByRole("button", { name: "+ Start a run" }).click();
  const dialog = page.getByRole("dialog", { name: "Start a run" });
  await dialog.getByLabel("Pipeline", { exact: true }).fill("build");
  return dialog;
}

test("submission stays visibly pending past ten seconds and accepts a late success once", async ({ page }) => {
  let release!: () => void;
  const pending = new Promise<void>((resolve) => { release = resolve; });
  const keys: string[] = [];
  await admissionAPI(page, async (route) => {
    keys.push(route.request().postDataJSON().idempotency_key);
    await pending;
    await route.fulfill({ status: 201, json: { run_id: "late-run" } });
  });
  const dialog = await openForm(page);
  await page.clock.install();
  await dialog.getByRole("button", { name: "Run", exact: true }).click();
  await expect.poll(() => keys.length).toBe(1);
  await page.clock.fastForward(120_000);
  await expect(dialog.getByRole("status")).toContainText("Waiting for the controller");
  await expect(dialog.getByRole("button", { name: "Triggering..." })).toBeDisabled();
  await expect(dialog.getByRole("alert")).toHaveCount(0);
  release();
  await expect(dialog).not.toBeVisible();
  expect(keys).toHaveLength(1);
});

test("a lost admission response retries the same key and a new form starts a new request", async ({ page }) => {
  const admitted = new Map<string, string>();
  const requests: { idempotency_key: string; request: { pipeline: string } }[] = [];
  await admissionAPI(page, async (route) => {
    const body = route.request().postDataJSON();
    requests.push(body);
    if (!admitted.has(body.idempotency_key)) admitted.set(body.idempotency_key, `run-${admitted.size + 1}`);
    if (requests.length === 1) return route.abort("failed");
    await route.fulfill({ status: 201, json: { run_id: admitted.get(body.idempotency_key) } });
  });
  let dialog = await openForm(page);
  await dialog.getByRole("button", { name: "Run", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("Retry without changing this form");
  await dialog.getByRole("button", { name: "Run", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  expect(requests).toHaveLength(2);
  expect(requests[1].idempotency_key).toBe(requests[0].idempotency_key);
  expect(admitted.size).toBe(1);

  await page.getByRole("button", { name: "+ Start a run" }).click();
  dialog = page.getByRole("dialog", { name: "Start a run" });
  await dialog.getByLabel("Pipeline", { exact: true }).fill("test");
  await dialog.getByRole("button", { name: "Run", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  expect(requests[2].idempotency_key).not.toBe(requests[0].idempotency_key);
  expect(admitted.size).toBe(2);
});

test("editing an unconfirmed submission starts a new request", async ({ page }) => {
  const keys: string[] = [];
  await admissionAPI(page, async (route) => {
    keys.push(route.request().postDataJSON().idempotency_key);
    if (keys.length === 1) return route.abort("failed");
    await route.fulfill({ status: 201, json: { run_id: "changed-run" } });
  });
  const dialog = await openForm(page);
  await dialog.getByRole("button", { name: "Run", exact: true }).click();
  await expect(dialog.getByRole("alert")).toBeVisible();
  await dialog.getByLabel("Pipeline", { exact: true }).fill("test");
  await dialog.getByRole("button", { name: "Run", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  expect(keys).toHaveLength(2);
  expect(keys[1]).not.toBe(keys[0]);
});

test("Run now keeps waiting for late admission and retries a lost response with the same key", async ({ page }) => {
  let release!: () => void;
  const pending = new Promise<void>((resolve) => { release = resolve; });
  const keys: string[] = [];
  await admissionAPI(page, async (route) => {
    keys.push(route.request().postDataJSON().idempotency_key);
    if (keys.length === 1) return route.abort("failed");
    await pending;
    await route.fulfill({ status: 201, json: { run_id: "late-retry" } });
  }, true);
  await page.goto("/runs");
  const row = page.locator('[data-run-id="unfunded"]');
  await row.getByRole("button", { name: "Run now", exact: true }).click();
  await expect(page.getByText(/Could not confirm the rerun/)).toBeVisible();
  await page.clock.install();
  await row.getByRole("button", { name: "Run now", exact: true }).click();
  await expect.poll(() => keys.length).toBe(2);
  await page.clock.fastForward(120_000);
  await expect(row.getByRole("button", { name: "Starting..." })).toBeDisabled();
  release();
  await expect(page.getByText("Rerun (all nodes) queued as late-retry")).toBeVisible();
  expect(keys[1]).toBe(keys[0]);
});

for (const mode of ["Rerun all", "Rerun from failed"]) {
  test(`the toolbar ${mode} action retains its key and shows pending admission`, async ({ page }) => {
    let release!: () => void;
    const pending = new Promise<void>((resolve) => { release = resolve; });
    const keys: string[] = [];
    await admissionAPI(page, async (route) => {
      keys.push(route.request().postDataJSON().idempotency_key);
      if (keys.length === 1) return route.abort("failed");
      await pending;
      await route.fulfill({ status: 201, json: { run_id: "toolbar-retry" } });
    }, true);
    await page.goto("/runs");
    await page.getByRole("checkbox", { name: "select all", exact: true }).check();
    await page.getByRole("button", { name: "Rerun", exact: true }).click();
    await page.getByRole("menuitem", { name: mode, exact: false }).click();
    await expect(page.getByText(/Could not confirm the rerun/)).toBeVisible();
    await page.clock.install();
    await page.getByRole("button", { name: "Rerun", exact: true }).click();
    await page.getByRole("menuitem", { name: mode, exact: false }).click();
    await expect.poll(() => keys.length).toBe(2);
    await page.clock.fastForward(120_000);
    await expect(page.getByRole("button", { name: "Starting..." })).toBeDisabled();
    release();
    await expect(page.getByText(/queued as toolbar-retry/)).toBeVisible();
    expect(keys[1]).toBe(keys[0]);
  });
}

test("schedule Run now retries the same key and each pending row stays busy", async ({ page }) => {
  let release!: () => void;
  const pending = new Promise<void>((resolve) => { release = resolve; });
  const keys: string[] = [];
  const schedules = ["alpha", "beta"].map((id) => ({
    id, name: `demo/${id}`, schedule_name: "default", repo_path: "demo",
    pipeline: id, where: "controller", cron: "0 3 * * *", tz: "UTC",
    overlap: "skip", catch_up_ns: 0, args: {}, paused: false, declared: true,
    state: "armed", state_detail: "", last_outcome: "", last_fired_at: "",
    lock: { ref: "", binary: "", digest: "", state: "follows" },
    override: { fields: [], stale: false, set_at: "" },
  }));
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (route.request().method() === "POST") {
      if (path === "/api/v1/schedules/alpha/run") {
        keys.push(route.request().headers()["idempotency-key"]);
        if (keys.length === 1) return route.abort("failed");
        await pending;
      }
      return route.fulfill({ json: { run_id: path.includes("alpha") ? "alpha-run" : "beta-run" } });
    }
    return route.fulfill({ json: path === "/api/v1/schedules" ? { schedules } : {} });
  });
  await page.goto("/crons");
  const alpha = page.locator('[data-schedule-id="alpha"]');
  const beta = page.locator('[data-schedule-id="beta"]');
  await alpha.getByRole("button", { name: "Run now", exact: true }).click();
  await expect(page.getByText(/Choose Run now again/)).toBeVisible();
  await page.clock.install();
  await alpha.getByRole("button", { name: "Run now", exact: true }).click();
  await expect.poll(() => keys.length).toBe(2);
  await beta.getByRole("button", { name: "Run now", exact: true }).click();
  await expect(page.getByText("Started beta-run", { exact: true })).toBeVisible();
  await page.clock.fastForward(120_000);
  await expect(alpha.getByRole("button", { name: "Working..." })).toBeDisabled();
  await expect(alpha.getByRole("button", { name: "Pause", exact: true })).toBeDisabled();
  await expect(beta.getByRole("button", { name: "Run now", exact: true })).toBeEnabled();
  release();
  await expect(page.getByText("Started alpha-run", { exact: true })).toBeVisible();
  expect(keys[1]).toBe(keys[0]);
});
