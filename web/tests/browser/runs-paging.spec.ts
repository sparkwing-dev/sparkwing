import { expect, test, type Page } from "@playwright/test";
import { startAuthenticatedDashboard } from "./authenticated-server";

const SEEDED = 230;
const base = Date.parse("2026-09-01T00:00:00Z");

function runID(i: number): string {
  return `paged-${String(i).padStart(4, "0")}`;
}

// The fixture's control origin records finished runs straight into the store,
// because a real run finishes only by executing on a runner.
async function seedRuns(
  controlOrigin: string,
  firstIndex: number,
  count: number,
  branch = "main",
): Promise<void> {
  const res = await fetch(`${controlOrigin}/__fixture/runs`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      count,
      first_index: firstIndex,
      id_prefix: "paged-",
      pipeline: "paging",
      branch,
      // A sub-millisecond part, so a cursor rounded to a JavaScript number would miss.
      start: new Date(base).toISOString().replace(".000Z", ".000123456Z"),
      step_seconds: 60,
    }),
  });
  if (res.status !== 200) {
    throw new Error(`seeding ${runID(firstIndex)}: ${res.status} ${await res.text()}`);
  }
}

async function shownRunIDs(page: Page): Promise<string[]> {
  return page
    .getByLabel("Runs pane")
    .locator("[data-run-id]")
    .evaluateAll((rows) =>
      rows.map((row) => (row as HTMLElement).dataset.runId ?? ""),
    );
}

test("the runs page pages past 200 runs by cursor and polls only the newest page", async ({
  page,
}) => {
  test.setTimeout(180_000);
  const dashboard = await startAuthenticatedDashboard();
  try {
    await seedRuns(dashboard.control_origin, 0, 3);
    await seedRuns(dashboard.control_origin, 3, 1, "rare");
    await seedRuns(dashboard.control_origin, 4, SEEDED - 4);
    const lists: URL[] = [];
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (request.method() === "GET" && url.pathname === "/api/v1/runs")
        lists.push(url);
    });

    await page.goto(`${dashboard.origin}/runs`);
    const login = page.locator('form[action="/login"]');
    await login.locator('input[name="token"]').fill(dashboard.token);
    await login.getByRole("button", { name: "Sign in", exact: true }).click();
    await expect(page).toHaveURL(`${dashboard.origin}/runs`);

    await expect.poll(() => shownRunIDs(page)).toHaveLength(50);
    expect((await shownRunIDs(page))[0]).toBe(runID(SEEDED - 1));
    await expect(page.getByRole("button", { name: "‹ Newer" })).toBeDisabled();

    const older = page.getByRole("button", { name: "Older ›" });
    for (let p = 1; p < 5; p++) {
      await older.click();
      await expect
        .poll(async () => (await shownRunIDs(page))[0])
        .toBe(runID(SEEDED - 1 - p * 50));
    }
    expect(await shownRunIDs(page)).toEqual(
      Array.from({ length: 30 }, (_, i) => runID(29 - i)),
    );
    await expect(older).toBeDisabled();
    expect(new URL(page.url()).searchParams.get("older")).toMatch(
      /^\d+:paged-0030$/,
    );

    // A deep link reopens the same page.
    await page.reload();
    await expect
      .poll(async () => (await shownRunIDs(page)).at(-1))
      .toBe(runID(0));

    // An older page loads once and waits to be asked.
    const listedBefore = lists.length;
    await page.waitForTimeout(5_000);
    expect(lists.length).toBe(listedBefore);
    await page.getByRole("button", { name: "↻ Refresh" }).click();
    await expect.poll(() => lists.length).toBe(listedBefore + 1);

    await page.getByRole("button", { name: "‹ Newer" }).click();
    await expect.poll(async () => (await shownRunIDs(page))[0]).toBe(runID(79));
    expect(await shownRunIDs(page)).toHaveLength(50);
    await page.goBack();
    await expect.poll(async () => (await shownRunIDs(page))[0]).toBe(runID(29));

    await page.getByRole("button", { name: "« Newest" }).click();
    await expect
      .poll(async () => (await shownRunIDs(page))[0])
      .toBe(runID(SEEDED - 1));
    expect(new URL(page.url()).searchParams.has("older")).toBe(false);

    // A new run lands on the newest page without moving what the reader is looking at.
    const pane = page.getByLabel("Runs pane");
    await pane.evaluate((el) => {
      el.scrollTop = 10 * 56;
    });
    const anchor = page.locator(`[data-run-id="${runID(SEEDED - 11)}"]`);
    const anchorTop = async () =>
      anchor.evaluate((row) => row.getBoundingClientRect().top);
    const before = await anchorTop();
    await seedRuns(dashboard.control_origin, SEEDED, 1);
    await expect
      .poll(async () => (await shownRunIDs(page))[0])
      .toBe(runID(SEEDED));
    expect(Math.abs((await anchorTop()) - before)).toBeLessThanOrEqual(1);

    // A filter selects across every run, not the page on screen.
    await page.goto(`${dashboard.origin}/runs?branch=rare`);
    await expect.poll(() => shownRunIDs(page)).toEqual([runID(3)]);
    expect(lists.at(-1)?.searchParams.get("git_branch")).toBe("rare");
  } finally {
    await dashboard.close();
  }
});
