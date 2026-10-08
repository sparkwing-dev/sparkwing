import { expect, test, type Page } from "@playwright/test";
import { startAuthenticatedDashboard } from "./authenticated-server";

const SEEDED = 230;
const base = Date.parse("2026-09-01T00:00:00Z");

function runID(i: number): string {
  return `paged-${String(i).padStart(3, "0")}`;
}

async function createRun(
  origin: string,
  token: string,
  i: number,
  branch = "main",
): Promise<void> {
  const res = await fetch(`${origin}/api/v1/runs`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      id: runID(i),
      pipeline: "paging",
      status: "success",
      git_branch: branch,
      // A sub-millisecond part, so a cursor rounded to a JavaScript number would miss.
      started_at: new Date(base + i * 60_000).toISOString().replace(".000Z", ".000123456Z"),
    }),
  });
  if (res.status !== 201) {
    throw new Error(`seeding ${runID(i)}: ${res.status} ${await res.text()}`);
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
    for (let i = 0; i < SEEDED; i++) {
      await createRun(
        dashboard.origin,
        dashboard.admin_token,
        i,
        i === 3 ? "rare" : "main",
      );
    }
    const lists: URL[] = [];
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (request.method() === "GET" && url.pathname === "/api/v1/runs")
        lists.push(url);
    });

    await page.goto(`${dashboard.origin}/runs`);
    const login = page.locator('form[action="/login"]');
    await login.locator('input[name="username"]').fill("admin");
    await login.locator('input[name="password"]').fill("correct-horse");
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
      /^\d+:paged-030$/,
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
    await createRun(dashboard.origin, dashboard.admin_token, SEEDED);
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
