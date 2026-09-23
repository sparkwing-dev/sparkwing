import AxeBuilder from "@axe-core/playwright";
import { type Page, type Route } from "@playwright/test";
import { expect, test } from "./fixtures";
import { startStaticDashboard } from "./static-server";



const finishedRun = {
  id: "run-20260827-001",
  pipeline: "deploy-production",
  status: "success",
  trigger_source: "github",
  git_branch: "main",
  git_sha: "abc1234def5678",
  repo: "sparkwing-dev/sparkwing",
  started_at: "2026-08-27T18:00:00Z",
  finished_at: "2026-08-27T18:01:30Z",
};

const runningRun = {
  ...finishedRun,
  id: "run-20260827-002",
  pipeline: "pre-commit",
  status: "running",
  started_at: "2026-08-27T18:05:00Z",
  finished_at: undefined,
};

const finishedDetail = {
  run: finishedRun,
  nodes: [
    {
      id: "verify",
      status: "success",
      outcome: "success",
      deps: [],
      started_at: "2026-08-27T18:00:00Z",
      finished_at: "2026-08-27T18:01:30Z",
      duration_ms: 90_000,
      decorations: {
        work: {
          steps: [
            {
              id: "tests",
              status: "passed",
              started_at: "2026-08-27T18:00:00Z",
              finished_at: "2026-08-27T18:01:30Z",
              duration_ms: 90_000,
            },
          ],
        },
      },
    },
  ],
};

test("keeps a long node name visible beside its location icon", async ({ page }) => {
  const longName = "verify-production-checks";
  const detail = {
    ...finishedDetail,
    nodes: [{
      ...finishedDetail.nodes[0],
      id: longName,
      executor_kind: "agent",
      executor_name: "moonborn",
      executor_location: "local",
    }],
  };
  await installMockAPI(page, {
    runs: [finishedRun],
    details: { [finishedRun.id]: detail },
  });
  await page.goto(`/runs?run=${finishedRun.id}`);
  const row = page.locator(`[data-node-id="${longName}"]`).first();
  await expect(row).toBeVisible();
  await expect(row.getByText(longName, { exact: true })).toBeVisible();
  const label = row.getByText(longName, { exact: true });
  expect(await label.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
  await expect(row.getByText("Local", { exact: true })).toHaveCount(0);
  const rowSite = row.getByLabel("Ran on moonborn (your machine)");
  await expect(rowSite).toBeVisible();
  await rowSite.focus();
  await expect(rowSite.getByRole("tooltip")).toBeVisible();
  await page.getByRole("button", { name: /DAG/ }).click();
  const dagSite = page.locator('svg [role="img"][aria-label="Ran on moonborn (your machine)"]');
  await expect(dagSite).toBeVisible();
  await dagSite.hover();
  await expect(page.locator("svg text", { hasText: "Ran on moonborn (your machine)" })).toBeVisible();
  await page.mouse.move(0, 0);
  await dagSite.focus();
  await expect(page.locator("svg text", { hasText: "Ran on moonborn (your machine)" })).toBeVisible();
});

function isoFromNow(ms: number): string {
  return new Date(Date.now() + ms).toISOString();
}

const armedCron = {
  id: "crn_0123456789ab",
  name: "dotfiles/nightly-vault-sweep",
  schedule_name: "default",
  repo_path: "/home/me/code/dotfiles",
  pipeline: "nightly-vault-sweep",
  where: "local",
  cron: "0 3 * * *",
  tz: "America/Denver",
  overlap: "skip",
  catch_up_ns: 3_600_000_000_000,
  args: {},
  lock: { ref: "", binary: "", digest: "", state: "follows" },
  override: { fields: [], stale: false, set_at: "" },
  effective: {
    cron: "0 3 * * *",
    tz: "America/Denver",
    overlap: "skip",
    catch_up_ns: 3_600_000_000_000,
    args: {},
  },
  paused: false,
  declared: true,
  state: "armed",
  state_detail: "",
  armed_at: isoFromNow(-4 * 3_600_000),
  updated_at: isoFromNow(-60_000),
  last_fired_at: isoFromNow(-9 * 3_600_000),
  last_run_id: "run_abc",
  last_outcome: "fired",
  next_due_at: isoFromNow(4 * 3_600_000),
};

const pausedCron = {
  ...armedCron,
  id: "crn_ffeeddccbbaa",
  name: "sparkwing/weekly-bench/host",
  schedule_name: "host",
  repo_path: "/home/me/code/sparkwing",
  pipeline: "weekly-bench",
  cron: "0 4 * * 0",
  effective: { ...armedCron.effective, cron: "0 4 * * 0" },
  paused: true,
  state: "paused",
  last_outcome: "skipped_overlap",
  last_run_id: "",
  last_fired_at: null,
};

const undeclaredCron = {
  ...armedCron,
  id: "crn_aabbccddeeff",
  name: "dotfiles/retired-sweep",
  pipeline: "retired-sweep",
  declared: false,
  state: "undeclared",
  next_due_at: null,
};

// A schedule running a pinned binary the checkout has since moved past.
const aheadCron = {
  ...armedCron,
  id: "crn_beefcafe0011",
  name: "dotfiles/nightly-rebuild",
  pipeline: "nightly-rebuild",
  lock: {
    ref: "abc1234def567890",
    binary: "/home/me/.sparkwing/crons/crn_beefcafe0011/pipeline",
    digest: "sha256:2f6c",
    state: "ahead",
  },
  state_detail: "locked, checkout ahead",
};

// A schedule this host runs on its own expression, set against a declaration
// the repo has changed since.
const overriddenCron = {
  ...armedCron,
  id: "crn_0f0f0f0f0f0f",
  name: "dotfiles/vault-sweep",
  pipeline: "vault-sweep",
  args: { depth: "deep" },
  override: {
    fields: ["cron"],
    stale: true,
    set_at: isoFromNow(-72 * 3_600_000),
  },
  effective: {
    ...armedCron.effective,
    cron: "0 5 * * *",
    args: { depth: "deep" },
  },
};

const healthyCronTimer = {
  installed: true,
  foreign: false,
  enabled: true,
  stale: false,
  path: "/home/me/.config/systemd/user/sparkwing-crons.timer",
  binary: "/home/me/.local/bin/sparkwing",
  detail: "timer enabled; last tick 12s ago",
};

const healthyCronHealth = {
  timer: healthyCronTimer,
  last_tick: {
    at: isoFromNow(-12_000),
    host: "wsl",
    version: "v0.42.0",
    error: "",
  },
  tick_stale: false,
  schedules: 3,
  armed: 1,
  paused: 1,
  undeclared: 1,
  locked: 0,
  following: 3,
  ahead: 0,
  missing_binary: 0,
  stale_override: 0,
  detail: "1 schedule armed on this host",
  remedy: "",
};

const cronsOverview = {
  health: healthyCronHealth,
  schedules: [armedCron, pausedCron, undeclaredCron],
};

const noCronsOverview = {
  health: {
    timer: {
      installed: false,
      foreign: false,
      enabled: false,
      stale: false,
      path: "",
      binary: "",
      detail: "no timer installed",
    },
    last_tick: { at: "", host: "", version: "", error: "" },
    tick_stale: false,
    schedules: 0,
    armed: 0,
    paused: 0,
    undeclared: 0,
    locked: 0,
    following: 0,
    ahead: 0,
    missing_binary: 0,
    stale_override: 0,
    detail: "no schedules armed on this host",
    remedy: "",
  },
  schedules: [],
};

const cronDetail = {
  schedule: armedCron,
  fires: [
    {
      id: "crf_02",
      schedule_id: armedCron.id,
      due_at: isoFromNow(-9 * 3_600_000),
      decided_at: isoFromNow(-9 * 3_600_000 + 800),
      outcome: "fired",
      run_id: "run_abc",
      detail: "",
      args: { depth: "deep" },
      run_status: "passed",
    },
    {
      id: "crf_01",
      schedule_id: armedCron.id,
      due_at: isoFromNow(-33 * 3_600_000),
      decided_at: isoFromNow(-33 * 3_600_000 + 600),
      outcome: "skipped_overlap",
      run_id: "",
      detail: "previous run still holding admission",
      args: {},
      run_status: "",
    },
  ],
  upcoming: [isoFromNow(4 * 3_600_000), isoFromNow(28 * 3_600_000)],
};

type MockAPIOptions = {
  runs?: Record<string, unknown>[];
  details?: Record<string, Record<string, unknown>>;
  agents?: Record<string, unknown>[];
  unauthorized?: boolean;
  failPath?: string;
  onDetail?: (route: Route, runID: string) => Promise<boolean>;
  onNodeMetrics?: (
    route: Route,
    runID: string,
    nodeID: string,
  ) => Promise<boolean>;
  nodeMetrics?: Record<string, Record<string, unknown>>;
  onEventStream?: (route: Route) => Promise<void>;
  onLogStream?: (route: Route) => Promise<void>;
  onRequest?: (route: Route) => void;
  crons?: Record<string, unknown> | (() => Record<string, unknown>);
  queue?: Record<string, unknown>;
  cronDetail?: Record<string, unknown>;
  onCronAction?: (id: string, action: string) => void;
};

const pageErrors = new WeakMap<Page, Error[]>();

test.beforeEach(async ({ page }) => {
  const errors: Error[] = [];
  pageErrors.set(page, errors);
  page.on("pageerror", (error) => errors.push(error));
});

test.afterEach(async ({ page }) => {
  expect(pageErrors.get(page) ?? []).toEqual([]);
});

async function installMockAPI(page: Page, options: MockAPIOptions = {}) {
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    options.onRequest?.(route);
    if (options.unauthorized) {
      await route.fulfill({ status: 401, json: { error: "unauthorized" } });
      return;
    }

    const url = new URL(request.url());
    const path = url.pathname;
    if (path === options.failPath) {
      await new Promise((resolve) => setTimeout(resolve, 100));
      await route.abort("connectionrefused");
      return;
    }
    const eventStreamMatch = path.match(
      /^\/api\/v1\/runs\/([^/]+)\/events\/stream$/,
    );
    if (request.method() === "GET" && eventStreamMatch) {
      if (options.onEventStream) {
        await options.onEventStream(route);
      } else {
        await route.fulfill({
          contentType: "text/event-stream",
          body: "event: stream_end\ndata: {}\n\n",
        });
      }
      return;
    }
    const logStreamMatch = path.match(
      /^\/api\/v1\/runs\/([^/]+)\/logs\/([^/]+)\/stream$/,
    );
    if (request.method() === "GET" && logStreamMatch) {
      if (options.onLogStream) {
        await options.onLogStream(route);
      } else {
        await route.fulfill({
          contentType: "text/event-stream",
          body: "",
        });
      }
      return;
    }
    if (request.method() === "GET" && path === "/api/v1/runs") {
      await route.fulfill({ json: { runs: options.runs ?? [] } });
      return;
    }
    if (request.method() === "GET" && path === "/api/v1/approvals/pending") {
      await route.fulfill({ json: { approvals: [] } });
      return;
    }
    if (request.method() === "GET" && path === "/api/v1/health/services") {
      await route.fulfill({ json: { services: [] } });
      return;
    }
    if (request.method() === "GET" && path === "/api/v1/agents") {
      await route.fulfill({ json: { agents: options.agents ?? [] } });
      return;
    }
    if (request.method() === "GET" && path === "/api/v1/pipelines") {
      await route.fulfill({ json: { pipelines: {} } });
      return;
    }
    if (request.method() === "GET" && path === "/api/v1/queue") {
      await route.fulfill({ json: options.queue ?? {} });
      return;
    }
    const readCrons = () =>
      (typeof options.crons === "function" ? options.crons() : options.crons) ??
      noCronsOverview;
    if (request.method() === "GET" && path === "/api/v1/crons") {
      await route.fulfill({ json: readCrons() });
      return;
    }
    const cronActionMatch = path.match(
      /^\/api\/v1\/crons\/([^/]+)\/(pause|resume|run)$/,
    );
    if (request.method() === "POST" && cronActionMatch) {
      const [, scheduleID, action] = cronActionMatch;
      options.onCronAction?.(scheduleID, action);
      const schedules = (readCrons().schedules ??
        []) as Record<string, unknown>[];
      const schedule =
        schedules.find((row) => row.id === scheduleID) ?? schedules[0] ?? {};
      await route.fulfill({
        json:
          action === "run"
            ? { run_id: "run_now", schedule }
            : { schedule },
      });
      return;
    }
    const cronDetailMatch = path.match(/^\/api\/v1\/crons\/([^/]+)$/);
    if (request.method() === "GET" && cronDetailMatch) {
      await route.fulfill(
        options.cronDetail
          ? { json: options.cronDetail }
          : { status: 404, body: "unknown schedule" },
      );
      return;
    }
    if (request.method() === "GET" && path.endsWith("/attempts")) {
      await route.fulfill({ json: { runs: [] } });
      return;
    }
    if (request.method() === "GET" && path.endsWith("/events")) {
      await route.fulfill({ json: [] });
      return;
    }
    if (request.method() === "GET" && path.endsWith("/paused")) {
      await route.fulfill({ json: [] });
      return;
    }
    const metricsMatch = path.match(
      /^\/api\/v1\/runs\/([^/]+)\/nodes\/([^/]+)\/metrics$/,
    );
    if (request.method() === "GET" && metricsMatch) {
      const [, runID, nodeID] = metricsMatch;
      if (
        options.onNodeMetrics &&
        (await options.onNodeMetrics(route, runID, nodeID))
      ) {
        return;
      }
      await route.fulfill({
        json: options.nodeMetrics?.[runID]?.[nodeID] ?? { points: [] },
      });
      return;
    }
    const detailMatch = path.match(/^\/api\/v1\/runs\/([^/]+)$/);
    if (request.method() === "GET" && detailMatch) {
      if (options.onDetail && (await options.onDetail(route, detailMatch[1]))) {
        return;
      }
      const detail = options.details?.[detailMatch[1]];
      await route.fulfill(detail ? { json: detail } : { status: 404, json: {} });
      return;
    }
    const logMatch = path.match(/^\/api\/v1\/runs\/([^/]+)\/logs\/([^/]+)$/);
    if (request.method() === "GET" && logMatch) {
      await route.fulfill({
        contentType: "application/x-ndjson",
        body: [
          JSON.stringify({
            ts: "2026-08-27T18:00:00Z",
            level: "info",
            node: "verify",
            event: "node_start",
          }),
          JSON.stringify({
            ts: "2026-08-27T18:00:00.050Z",
            level: "info",
            node: "verify",
            event: "exec_line",
            msg: "PREAMBLE stays outside the tests step",
          }),
          JSON.stringify({
            ts: "2026-08-27T18:00:00.100Z",
            level: "info",
            node: "verify",
            event: "step_start",
            msg: "tests",
          }),
          JSON.stringify({
            ts: "2026-08-27T18:00:00.200Z",
            level: "info",
            node: "verify",
            step: "tests",
            event: "exec_line",
            msg: "PASS dashboard smoke",
          }),
          JSON.stringify({
            ts: "2026-08-27T18:00:01.600Z",
            level: "info",
            node: "verify",
            event: "step_end",
            msg: "tests",
            attrs: { outcome: "success", duration_ms: 1500 },
          }),
          JSON.stringify({
            ts: "2026-08-27T18:00:01.600Z",
            level: "info",
            node: "verify",
            event: "node_end",
            attrs: { outcome: "success", duration_ms: 1600 },
          }),
        ].join("\n"),
      });
      return;
    }
    if (request.method() === "POST" && path.endsWith("/cancel")) {
      await route.fulfill({ status: 204, body: "" });
      return;
    }
    if (request.method() === "POST" && path.endsWith("/retry")) {
      await route.fulfill({ json: { ...finishedRun, id: "run-retry" } });
      return;
    }
    await route.fulfill({ status: 404, json: {} });
  });
}

async function openCollapsedLogFocus(
  page: Page,
  stepID: string | null,
): Promise<void> {
  await page.addInitScript(() => {
    const targetWindow = window as typeof window & {
      __SPARKWING_TEST_SCROLLED_LINES__?: string[];
    };
    targetWindow.__SPARKWING_TEST_SCROLLED_LINES__ = [];
    HTMLElement.prototype.scrollIntoView = function () {
      const line = this.dataset.line;
      if (line) targetWindow.__SPARKWING_TEST_SCROLLED_LINES__?.push(line);
    };
  });
  await installMockAPI(page, {
    runs: [finishedRun],
    details: { [finishedRun.id]: finishedDetail },
  });
  await page.goto("/runs");
  await expect(
    page.locator(`[data-run-id="${finishedRun.id}"]`),
  ).toBeVisible();
  await page.evaluate(
    ({ focusStep }) => {
      sessionStorage.setItem(
        "sparkwing.searchResultFocus",
        JSON.stringify({ nodeID: "verify", stepID: focusStep, line: 2 }),
      );
    },
    { focusStep: stepID },
  );
  await page.goto(`/runs?run=${finishedRun.id}&node=verify`);
}

async function expectExactLineFocus(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Logs", exact: true }).click();
  await expect(
    page
      .locator('[data-step-id="tests"]')
      .getByText("PASS dashboard smoke", { exact: true }),
  ).toBeVisible();
  await expect
    .poll(() =>
      page.evaluate(
        () =>
          (
            window as typeof window & {
              __SPARKWING_TEST_SCROLLED_LINES__?: string[];
            }
          ).__SPARKWING_TEST_SCROLLED_LINES__ ?? [],
      ),
    )
    .toContain("2");
}

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

async function selectRunWhileOlderDetailIsPending(
  page: Page,
  newerDetail: Record<string, unknown> | null,
): Promise<() => Promise<void>> {
  const olderStarted = deferred();
  const olderRelease = deferred();
  const olderDelivered = deferred();
  const newerFinished = deferred();
  let olderFailure: string | null = null;
  await installMockAPI(page, {
    runs: [
      finishedRun,
      newerDetail
        ? runningRun
        : {
            ...runningRun,
            status: "failed",
            finished_at: "2026-08-27T18:06:00Z",
          },
    ],
    details: newerDetail ? { [runningRun.id]: newerDetail } : undefined,
    onDetail: async (route, runID) => {
      if (runID === runningRun.id && !newerDetail) {
        await route.abort("connectionrefused");
        newerFinished.resolve();
        return true;
      }
      if (runID !== finishedRun.id) return false;
      olderStarted.resolve();
      await olderRelease.promise;
      try {
        await route.fulfill({ json: finishedDetail });
      } catch (error) {
        olderFailure = String(error);
      }
      olderDelivered.resolve();
      return true;
    },
  });
  await page.goto("/runs");
  await page.locator(`[data-run-id="${finishedRun.id}"]`).click();
  await olderStarted.promise;
  await page.locator(`[data-run-id="${runningRun.id}"]`).click();
  if (!newerDetail) await newerFinished.promise;

  return async () => {
    olderRelease.resolve();
    await olderDelivered.promise;
    if (olderFailure) {
      throw new Error(`older detail request was not delivered: ${olderFailure}`);
    }
    await page.evaluate(
      () =>
        new Promise<void>((resolve) =>
          requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
        ),
    );
  };
}

test("renders the empty production dashboard and passes accessibility smoke", async ({
  page,
}) => {
  await installMockAPI(page);
  await page.goto("/");

  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  await expect(page.getByText("No completed deploys yet.")).toBeVisible();
  await expect(
    page.getByText("Nothing needs attention. Services healthy, no pending approvals."),
  ).toBeVisible();

  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa"])
    .analyze();
  expect(
    results.violations.filter((violation) =>
      ["serious", "critical"].includes(violation.impact ?? ""),
    ),
  ).toEqual([]);
});

test("surfaces controller authentication failures", async ({ page }) => {
  await installMockAPI(page, { unauthorized: true });
  await page.goto("/");

  await expect(page.getByText("Authentication failed", { exact: true })).toBeVisible();
  await expect(page.getByText(/API token is missing or invalid/)).toBeVisible();
});

test("opens a completed run and renders stored node logs", async ({ page }) => {
  await installMockAPI(page, {
    runs: [finishedRun],
    details: { [finishedRun.id]: finishedDetail },
  });
  await page.goto("/runs");

  await page.locator(`[data-run-id="${finishedRun.id}"]`).click();
  await expect(page).toHaveURL(new RegExp(`run=${finishedRun.id}`));
  await expect(page.getByText(`Nodes (1)`, { exact: true })).toBeVisible();
  await page.locator('[data-node-id="verify"]').first().click();
  await page.getByRole("button", { name: /^DAG/ }).click();
  await expect(page.locator('[data-step-id="tests"]')).toBeVisible();
  await page.getByRole("button", { name: "Logs", exact: true }).click();
  const structuredStep = page.locator('[data-step-id="tests"]');
  await expect(structuredStep.getByText("tests", { exact: true })).toBeVisible();
  await expect(structuredStep).toContainText("1.5s");
  await structuredStep.getByRole("button").click();
  await expect(
    structuredStep.getByText("PASS dashboard smoke", { exact: true }),
  ).toBeVisible();
  await expect(
    structuredStep.getByText("PREAMBLE stays outside the tests step", {
      exact: true,
    }),
  ).toHaveCount(0);
  await expect(
    page.getByText("PREAMBLE stays outside the tests step", { exact: true }),
  ).toBeVisible();
});

test("keeps interval, command, and process resource evidence distinct", async ({
  page,
}) => {
  const detail = {
    ...finishedDetail,
    nodes: finishedDetail.nodes.map((node) => ({
      ...node,
      requested_cores: 2.5,
      requested_memory_bytes: 4 * 2 ** 30,
      cpu_nanos: 3_000_000_000,
      process_wall_nanos: 2_000_000_000,
      max_rss_bytes: 900 * 2 ** 20,
    })),
  };
  await installMockAPI(page, {
    runs: [finishedRun],
    details: { [finishedRun.id]: detail },
    nodeMetrics: {
      [finishedRun.id]: {
        verify: {
          points: [
            {
              ts: "2026-08-27T18:00:02Z",
              cpu_millicores: 750,
              memory_bytes: 500 * 2 ** 20,
            },
            {
              ts: "2026-08-27T18:00:03Z",
              cpu_millicores: 2200,
              memory_bytes: 800 * 2 ** 20,
              cpu_time_nanos: 1_250_000_000,
            },
          ],
        },
      },
    },
  });
  await page.goto(
    `/runs?run=${finishedRun.id}&node=verify&tab=resources`,
  );
  await page.getByRole("button", { name: "Resources", exact: true }).click();

  await expect(page.getByText("Resource evidence", { exact: true })).toBeVisible();
  await expect(page.getByText(/Interval peak CPU:\s*750m/)).toBeVisible();
  await expect(
    page.getByText(/Command peak average CPU:\s*2\.2 CPU/),
  ).toBeVisible();
  await expect(page.getByText(/Process CPU time:\s*3\.00s/)).toBeVisible();
  await expect(page.getByText(/Command CPU time:\s*1\.25s/)).toBeVisible();
  await expect(page.getByText(/Interval peak memory:\s*500Mi/)).toBeVisible();
  await expect(page.getByText(/Process max RSS:\s*900Mi/)).toBeVisible();
  await expect(page.getByText(/Command max RSS:\s*800Mi/)).toBeVisible();
  await expect(page.getByText("1 interval reading", { exact: true })).toBeVisible();
  await expect(page.getByText("1 command report", { exact: true })).toBeVisible();
});

test("names a cache hit without requesting execution metrics", async ({ page }) => {
  let metricRequests = 0;
  const detail = {
    ...finishedDetail,
    nodes: finishedDetail.nodes.map((node) => ({
      ...node,
      outcome: "cached",
      duration_ms: 0,
    })),
  };
  await installMockAPI(page, {
    runs: [finishedRun],
    details: { [finishedRun.id]: detail },
    onNodeMetrics: async () => {
      metricRequests++;
      return false;
    },
  });
  await page.goto(`/runs?run=${finishedRun.id}&node=verify`);
  await page.getByRole("button", { name: "Resources", exact: true }).click();

  await expect(page.getByText(/Cache hit\. Sparkwing reused/)).toBeVisible();
  expect(metricRequests).toBe(0);
});

test("ignores an older node metrics response after switching runs", async ({
  page,
}) => {
  const newerRun = {
    ...finishedRun,
    id: "run-20260827-newer",
    pipeline: "pre-commit",
  };
  const newerDetail = { ...finishedDetail, run: newerRun };
  const olderStarted = deferred();
  const olderRelease = deferred();
  const olderDelivered = deferred();
  await installMockAPI(page, {
    runs: [finishedRun, newerRun],
    details: {
      [finishedRun.id]: finishedDetail,
      [newerRun.id]: newerDetail,
    },
    onNodeMetrics: async (route, runID) => {
      if (runID === finishedRun.id) {
        olderStarted.resolve();
        await olderRelease.promise;
        await route.fulfill({
          json: {
            points: [
              {
                ts: "2026-08-27T18:00:02Z",
                cpu_millicores: 111,
                memory_bytes: 111 * 2 ** 20,
              },
            ],
          },
        });
        olderDelivered.resolve();
        return true;
      }
      await route.fulfill({
        json: {
          points: [
            {
              ts: "2026-08-27T18:00:02Z",
              cpu_millicores: 2200,
              memory_bytes: 700 * 2 ** 20,
            },
          ],
        },
      });
      return true;
    },
  });
  await page.goto("/runs");
  await page.locator(`[data-run-id="${finishedRun.id}"]`).click();
  await page.getByRole("button", { name: "Resources", exact: true }).click();
  await page.locator('[data-resource-node-id="verify"]').click();
  await olderStarted.promise;

  await page.locator(`[data-run-id="${newerRun.id}"]`).click();
  await page.locator('[data-resource-node-id="verify"]').click();
  await expect(page.getByText(/Interval peak CPU:\s*2\.2 CPU/)).toBeVisible();
  olderRelease.resolve();
  await olderDelivered.promise;
  await page.evaluate(
    () =>
      new Promise<void>((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
      ),
  );

  await expect(page.getByText(/Interval peak CPU:\s*2\.2 CPU/)).toBeVisible();
  await expect(page.getByText(/Interval peak CPU:\s*111m/)).toHaveCount(0);
});

test("shows an empty DAG canvas with the error for a run that never planned", async ({
  page,
}) => {
  const refused = {
    id: "run-20260909-003",
    pipeline: "heartbeat",
    status: "failed",
    started_at: "2026-09-09T03:00:00Z",
    finished_at: "2026-09-09T03:00:01Z",
    error:
      "local dispatch: child exec: exit status 1: wingd/client: daemon build differs from this client",
  };
  await installMockAPI(page, {
    runs: [refused],
    details: { [refused.id]: { run: refused, nodes: [] } },
  });
  await page.goto(`/runs?run=${refused.id}`);

  await page.getByRole("button", { name: /^DAG/ }).click();
  const empty = page.getByTestId("dag-empty");
  await expect(empty).toBeVisible();
  await expect(empty).toContainText("No DAG for this run");
  await expect(empty).toContainText("ended before its pipeline planned any nodes");
  await expect(empty).toContainText("daemon build differs from this client");
});

test("keeps the selected run when an older detail request finishes late", async ({
  page,
}) => {
  const newerDetail = {
    run: runningRun,
    nodes: [
      ...finishedDetail.nodes,
      { id: "package", status: "running", outcome: "", deps: ["verify"] },
    ],
  };
  const finishOlderRequest = await selectRunWhileOlderDetailIsPending(
    page,
    newerDetail,
  );
  await expect(page.getByText("Nodes (2)", { exact: true })).toBeVisible();
  await finishOlderRequest();
  await expect(page.getByText("Nodes (2)", { exact: true })).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`run=${runningRun.id}`));
});

test("does not restore an older run after the new detail request fails", async ({
  page,
}) => {
  const finishOlderRequest = await selectRunWhileOlderDetailIsPending(
    page,
    null,
  );
  await expect(page).toHaveURL(new RegExp(`run=${runningRun.id}`));
  await finishOlderRequest();
  await expect(page.getByText("Nodes (1)", { exact: true })).toHaveCount(0);
  await expect(page).toHaveURL(new RegExp(`run=${runningRun.id}`));
});

test("opens a selected DAG step when logs mount", async ({ page }) => {
  await installMockAPI(page, {
    runs: [finishedRun],
    details: { [finishedRun.id]: finishedDetail },
  });
  await page.goto(`/runs?run=${finishedRun.id}`);

  await page.locator('[data-node-id="verify"]').first().click();
  await page.getByRole("button", { name: /^DAG/ }).click();
  const dagStep = page.locator('[data-step-id="tests"]');
  await expect(dagStep).toBeVisible();
  await dagStep.click();
  await page.getByRole("button", { name: "Logs", exact: true }).click();

  await expect(
    page
      .locator('[data-step-id="tests"]')
      .getByText("PASS dashboard smoke", { exact: true }),
  ).toBeVisible();
});

test("focuses an exact line after expanding its selected step", async ({
  page,
}) => {
  await openCollapsedLogFocus(page, "tests");
  await expectExactLineFocus(page);
});

test("focuses an exact line without a selected step", async ({ page }) => {
  await openCollapsedLogFocus(page, null);
  await expectExactLineFocus(page);
});

test("memoized run pills follow URL filter state and toggle from it", async ({
  page,
}) => {
  await installMockAPI(page, { runs: [finishedRun] });
  await page.goto("/runs");

  const row = page.locator(`[data-run-id="${finishedRun.id}"]`);
  const pipelinePill = row
    .locator("span.cursor-pointer")
    .filter({ hasText: finishedRun.pipeline });
  await expect(pipelinePill).not.toHaveClass(/decoration-2/);

  await pipelinePill.click();
  await page
    .getByRole("button", {
      name: `+ filter to ${finishedRun.pipeline}`,
      exact: true,
    })
    .click();
  await expect(page).toHaveURL(/(?:\?|&)pipeline=deploy-production(?:&|$)/);
  await expect(pipelinePill).toHaveClass(/decoration-2/);

  await pipelinePill.click();
  await page
    .getByRole("button", {
      name: `✓ included ${finishedRun.pipeline}`,
      exact: true,
    })
    .click();
  await expect
    .poll(() => new URL(page.url()).searchParams.get("pipeline"))
    .toBeNull();
  await expect(pipelinePill).not.toHaveClass(/decoration-2/);
});

test("auto-expands the pipeline that owns a selected run", async ({ page }) => {
  await installMockAPI(page, { runs: [finishedRun] });
  await page.goto(`/runs?view=pipelines&run=${finishedRun.id}`);

  await expect(page.getByRole("heading", { name: "Pipelines" })).toBeVisible();
  await expect(
    page.locator(`[data-run-id="${finishedRun.id}"]`),
  ).toBeVisible();
  await expect
    .poll(() => new URL(page.url()).searchParams.get("exp"))
    .toBe("sparkwing/deploy-production");
});

test("run rows keep their height while the detail pane closes", async (
  { page },
  testInfo,
) => {
  const runs = Array.from({ length: 5 }, (_, index) => ({
    ...finishedRun,
    id: `run-collapse-${index}`,
    pipeline: `long-pipeline-name-${index}`,
    error: "A failed run with a long error message that should take up space in the full activity row layout when the detail pane closes, including additional context for the operator to inspect",
  }));
  await installMockAPI(page, {
    runs,
    details: Object.fromEntries(
      runs.map((run) => [run.id, { ...finishedDetail, run }]),
    ),
  });
  await page.goto("/runs");
  const rows = page.locator("[data-run-id]");
  await expect(rows).toHaveCount(runs.length);
  await rows.first().click();
  await expect(page).toHaveURL(/run=run-collapse-0/);
  await expect
    .poll(() =>
      rows
        .first()
        .evaluate((row) => row.parentElement!.parentElement!.getBoundingClientRect().width),
    )
    .toBe(208);

  // Start sampling before the click so the first transition frame is included.
  const capture = page.evaluate(async () => {
    const row = document.querySelector<HTMLElement>('[data-run-id="run-collapse-0"]')!;
    const heights: { height: number; width: number; pane: number }[] = [];
    for (let frame = 0; frame < 45; frame++) {
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      heights.push({
        height: row.getBoundingClientRect().height,
        width: row.getBoundingClientRect().width,
        pane: row.parentElement!.parentElement!.getBoundingClientRect().width,
      });
    }
    return heights;
  });
  await rows.first().click();
  const samples = await capture;
  await testInfo.attach("detail-close-row-heights.json", {
    body: JSON.stringify(samples),
    contentType: "application/json",
  });
  expect(Math.max(...samples.map((sample) => sample.height))).toBeLessThanOrEqual(
    samples.at(-1)!.height + 2,
  );
  await expect(rows.first().locator(".grid").first()).toBeVisible();

  await page.emulateMedia({ reducedMotion: "reduce" });
  await rows.first().click();
  await expect
    .poll(() =>
      rows
        .first()
        .evaluate((row) => row.parentElement!.parentElement!.getBoundingClientRect().width),
    )
    .toBe(208);
  await rows.first().click();
  await expect(rows.first().locator(".grid").first()).toBeVisible();
});

test("runs and nodes collapse into selectable rails and remember the viewer's choice", async ({ page }) => {
  await page.setViewportSize({ width: 1000, height: 800 });
  await installMockAPI(page, {
    runs: [runningRun, finishedRun],
    details: {
      [runningRun.id]: {
        run: runningRun,
        nodes: [
          { id: "compile", status: "success", outcome: "success", duration_ms: 1200 },
          { id: "publish", status: "running", outcome: "", duration_ms: 2300 },
        ],
      },
      [finishedRun.id]: finishedDetail,
    },
  });
  await page.goto(`/runs?run=${runningRun.id}`);

  const runsRail = page.getByLabel("Runs rail");
  const nodesRail = page.getByLabel("Nodes rail");
  await expect(runsRail.locator("[data-rail-id]")).toHaveCount(2);
  await expect(nodesRail.locator("[data-rail-id]")).toHaveCount(2);
  await expect.poll(() => runsRail.evaluate((rail) => rail.parentElement!.parentElement!.getBoundingClientRect().width)).toBe(32);
  await expect.poll(() => nodesRail.evaluate((rail) => rail.parentElement!.getBoundingClientRect().width)).toBe(32);
  await expect(runsRail.locator('[data-rail-id="run-20260827-002"]')).toHaveAttribute("aria-pressed", "true");
  await expect(runsRail.locator("[data-rail-id]").first()).toHaveAttribute("aria-label", /sparkwing-dev\/sparkwing.*pre-commit.*main.*2026/);
  await runsRail.locator('[data-rail-id="run-20260827-001"]').hover();
  await expect(page.getByRole("tooltip")).toContainText("deploy-production");
  await nodesRail.locator('[data-rail-id="publish"]').hover();
  await expect(page.getByRole("tooltip")).toContainText("publish · 2.3s");
  await runsRail.locator('[data-rail-id="run-20260827-001"]').click();
  await expect(page).toHaveURL(/run=run-20260827-001/);
  await nodesRail.locator('[data-rail-id="verify"]').click();
  await expect(page).toHaveURL(/node=verify/);
  await expect(nodesRail.locator('[data-rail-id="verify"]')).toHaveAttribute("aria-pressed", "true");

  await page.getByRole("button", { name: "Expand runs and nodes" }).click();
  await expect(runsRail).toHaveCount(0);
  await expect(page.locator('[data-run-id="run-20260827-001"]')).toBeVisible();
  await page.reload();
  await expect(runsRail).toHaveCount(0);
  await page.getByRole("button", { name: "Collapse runs and nodes" }).click();
  await expect(runsRail.locator("[data-rail-id]")).toHaveCount(2);
});

test("runs columns follow the viewport until the viewer chooses a width", async ({ page }) => {
  await page.setViewportSize({ width: 1300, height: 800 });
  await installMockAPI(page, {
    runs: [finishedRun],
    details: { [finishedRun.id]: finishedDetail },
  });
  await page.goto(`/runs?run=${finishedRun.id}`);
  const tab = page.getByRole("button", { name: "Activity" });
  const tabY = await tab.evaluate((element) => element.getBoundingClientRect().top);
  await expect(page.getByLabel("Runs rail")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Collapse runs and nodes" })).toBeVisible();

  await page.setViewportSize({ width: 1000, height: 800 });
  await expect(page.getByLabel("Runs rail")).toBeVisible();
  expect(await tab.evaluate((element) => element.getBoundingClientRect().top)).toBe(tabY);
  await page.getByRole("button", { name: "Expand runs and nodes" }).click();
  await expect(page.getByLabel("Runs rail")).toHaveCount(0);
  await page.setViewportSize({ width: 900, height: 800 });
  await expect(page.getByLabel("Runs rail")).toHaveCount(0);
});

test("renders live structured node logs", async ({ page }) => {
  let requestedFormat = "";
  const runningDetail = {
    ...finishedDetail,
    run: runningRun,
    nodes: finishedDetail.nodes.map((node) => ({
      ...node,
      status: "running",
      outcome: "",
      finished_at: undefined,
    })),
  };
  await installMockAPI(page, {
    runs: [runningRun],
    details: { [runningRun.id]: runningDetail },
    onLogStream: async (route) => {
      requestedFormat =
        new URL(route.request().url()).searchParams.get("format") ?? "";
      if (requestedFormat !== "ndjson") {
        await route.fulfill({
          contentType: "text/event-stream",
          body: "data: ANSI PREAMBLE: structured event envelopes were pretty-rendered\n\n",
        });
        return;
      }
      const body = [
        JSON.stringify({
          ts: "2026-08-27T18:05:00Z",
          level: "info",
          node: "verify",
          event: "node_start",
        }),
        JSON.stringify({
          ts: "2026-08-27T18:05:00.050Z",
          level: "info",
          node: "verify",
          event: "exec_line",
          msg: "LIVE PREAMBLE stays outside the tests step",
        }),
        JSON.stringify({
          ts: "2026-08-27T18:05:00Z",
          level: "info",
          node: "verify",
          event: "step_start",
          msg: "tests",
        }),
        JSON.stringify({
          ts: "2026-08-27T18:05:00.100Z",
          level: "info",
          node: "verify",
          step: "tests",
          event: "exec_line",
          msg: "LIVE dashboard smoke",
        }),
      ].join("\n");
      await route.fulfill({
        contentType: "text/event-stream",
        body: body
          .split("\n")
          .map((line) => `data: ${line}\n\n`)
          .join(""),
      });
    },
  });
  await page.goto(`/runs?run=${runningRun.id}`);
  await page.locator('[data-node-id="verify"]').first().click();
  await page.getByRole("button", { name: "Logs", exact: true }).click();

  const structuredStep = page.locator('[data-step-id="tests"]');
  await expect.poll(() => requestedFormat).toBe("ndjson");
  await expect(structuredStep.getByText("tests", { exact: true })).toBeVisible();
  await expect(
    structuredStep.getByText("LIVE dashboard smoke", { exact: true }),
  ).toBeVisible();
  await expect(
    structuredStep.getByText("LIVE PREAMBLE stays outside the tests step", {
      exact: true,
    }),
  ).toHaveCount(0);
  await expect(
    page.getByText("LIVE PREAMBLE stays outside the tests step", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText(/ANSI PREAMBLE/)).toHaveCount(0);
  await expect(page.getByText(/\"event\":\"step_start\"/)).toHaveCount(0);
});

test("resumes the run event stream after a disconnect", async ({ page }) => {
  const completedDetail: Record<string, unknown> = {
    ...finishedDetail,
    run: {
      ...runningRun,
      status: "success",
      finished_at: "2026-08-27T18:06:30Z",
    },
  };
  const runningDetail: Record<string, unknown> = {
    ...finishedDetail,
    run: runningRun,
    nodes: finishedDetail.nodes.map((node) => ({
      ...node,
      status: "running",
      outcome: "",
      finished_at: undefined,
    })),
  };
  const observed = {
    connections: 0,
    lastEventIDs: [] as (string | null)[],
    detailRequests: 0,
    detailRequestsAtReconnect: 0,
    reconnectAt: 0,
    resumedDetailAt: 0,
    resumed: false,
  };
  let baselineReady: (() => void) | undefined;
  const baseline = new Promise<void>((resolve) => {
    baselineReady = resolve;
  });
  const dashboard = await startStaticDashboard(async (request, response) => {
    const url = new URL(request.url ?? "/", "http://localhost");
    if (!url.pathname.startsWith("/api/v1/")) return false;

    const sendJSON = (body: unknown, status = 200) => {
      response.writeHead(status, { "Content-Type": "application/json" });
      response.end(JSON.stringify(body));
    };
    if (url.pathname === "/api/v1/runs") {
      sendJSON({ runs: [] });
      return true;
    }
    if (url.pathname === "/api/v1/pipelines") {
      sendJSON({ pipelines: {} });
      return true;
    }
    if (url.pathname === "/api/v1/approvals/pending") {
      sendJSON({ approvals: [] });
      return true;
    }
    if (url.pathname === "/api/v1/health/services") {
      sendJSON({ services: [] });
      return true;
    }
    if (url.pathname.endsWith("/attempts")) {
      sendJSON({ runs: [] });
      return true;
    }
    if (url.pathname.endsWith("/events")) {
      sendJSON([]);
      return true;
    }
    if (url.pathname.endsWith("/paused")) {
      sendJSON([]);
      return true;
    }
    if (url.pathname === `/api/v1/runs/${runningRun.id}`) {
      observed.detailRequests++;
      if (observed.detailRequests >= 2) baselineReady?.();
      if (observed.resumed && observed.resumedDetailAt === 0) {
        observed.resumedDetailAt = Date.now();
      }
      sendJSON(observed.resumed ? completedDetail : runningDetail);
      return true;
    }
    if (
      url.pathname === `/api/v1/runs/${runningRun.id}/events/stream`
    ) {
      observed.connections++;
      const lastEventID = request.headers["last-event-id"];
      observed.lastEventIDs.push(
        typeof lastEventID === "string" ? lastEventID : null,
      );
      response.writeHead(200, {
        "Cache-Control": "no-cache",
        "Content-Type": "text/event-stream",
      });
      if (observed.connections === 1) {
        response.write(
          [
            "retry: 100",
            "id: 41",
            "event: node_started",
            `data: ${JSON.stringify({
              run_id: runningRun.id,
              seq: 41,
              node_id: "verify",
              kind: "node_started",
              ts: "2026-08-27T18:05:00Z",
            })}`,
            "",
            "",
          ].join("\n"),
        );
        await baseline;
        response.end();
        return true;
      }
      observed.detailRequestsAtReconnect = observed.detailRequests;
      observed.reconnectAt = Date.now();
      if (lastEventID !== "41") {
        response.end();
        return true;
      }
      observed.resumed = true;
      response.end(
        [
          "id: 42",
          "event: node_succeeded",
          `data: ${JSON.stringify({
            run_id: runningRun.id,
            seq: 42,
            node_id: "verify",
            kind: "node_succeeded",
            ts: "2026-08-27T18:05:01Z",
          })}`,
          "",
          "id: 43",
          "event: stream_end",
          `data: ${JSON.stringify({
            run_id: runningRun.id,
            seq: 43,
            kind: "stream_end",
          })}`,
          "",
          "",
        ].join("\n"),
      );
      return true;
    }
    sendJSON({}, 404);
    return true;
  });
  try {
    await page.goto(`${dashboard.origin}/runs?run=${runningRun.id}`);

    await expect.poll(() => observed.connections, { timeout: 3_000 }).toBe(2);
    expect(observed.lastEventIDs).toEqual([null, "41"]);
    expect(observed.detailRequestsAtReconnect).toBe(2);
    await expect
      .poll(() => observed.detailRequests, { timeout: 2_000 })
      .toBe(observed.detailRequestsAtReconnect + 1);
    expect(observed.resumedDetailAt - observed.reconnectAt).toBeLessThan(2_000);
    await expect(
      page.locator('[data-node-id="verify"]').first(),
    ).toHaveAttribute("title", /verify · success/);
  } finally {
    await dashboard.close();
  }
});

test("lists a connection-only lease apart from the holders", async ({
  page,
}) => {
  await installMockAPI(page, {
    queue: {
      daemon_version: "v0.50.1",
      resources: [{ key: "cores", capacity: 16, held: 4 }],
      holders: [
        {
          run_id: "run-active",
          pipeline: "pre-push",
          elapsed_ms: 12_000,
          resources: {},
          connection_only: true,
        },
        {
          run_id: "run-active",
          participant_id: "run-active/node-host/dGVzdA",
          display_run_id: "run-active/pre-push",
          pipeline: "pre-push",
          elapsed_ms: 12_000,
          resources: { cores: 4 },
        },
      ],
      waiters: [],
    },
  });
  await page.goto("/queue");

  await expect(
    page.getByText("1 holding, 1 connected, 0 queued"),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Connected (no resources held)" }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "run-active/pre-push" }),
  ).toHaveCount(1);
  await expect(
    page.getByRole("link", { name: "run-active", exact: true }),
  ).toHaveCount(1);
});

test("surfaces a general controller connection failure", async ({ page }) => {
  await installMockAPI(page, { failPath: "/api/v1/queue" });
  await page.goto("/queue");

  await expect(
    page.getByText("Cannot reach the sparkwing controller", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Admission queue" }),
  ).toBeVisible();
});

test("shows retry lineage from privacy-safe execution attempts", async ({
  page,
}) => {
  const retriedDetail = {
    ...finishedDetail,
    nodes: finishedDetail.nodes.map((node) => ({
      ...node,
      run_id: finishedRun.id,
      claimed: true,
      executor_kind: "gateway",
      executor_name: "cloud-helper",
      executor_location: "cloud",
      execution_attempts: [
        {
          run_id: "run-original",
          node_id: node.id,
          attempt: 1,
          executor_kind: "agent",
          executor_name: "design-mac",
          location: "local",
          platform: "darwin/arm64",
          started_at: "2026-08-27T17:59:00Z",
          finished_at: "2026-08-27T17:59:20Z",
          outcome: "failed",
          failure_reason: "agent_lost",
          retry_run_id: finishedRun.id,
        },
        {
          run_id: finishedRun.id,
          node_id: node.id,
          attempt: 2,
          executor_kind: "gateway",
          executor_name: "cloud-helper",
          location: "cloud",
          started_at: "2026-08-27T18:00:00Z",
          finished_at: "2026-08-27T18:01:30Z",
          outcome: "success",
        },
      ],
    })),
  };
  await installMockAPI(page, {
    runs: [finishedRun],
    details: { [finishedRun.id]: retriedDetail },
  });

  await page.goto(`/runs?run=${finishedRun.id}&node=verify`);

  await expect(
    page.getByRole("region", { name: "Execution history for verify" }),
  ).toBeVisible();
  await expect(page.getByText("Attempt 2", { exact: true })).toBeVisible();
  await expect(page.getByText("Attempt 1", { exact: true })).toBeVisible();
  await expect(page.getByText("Platform darwin/arm64")).toBeVisible();
  await expect(page.getByText("failure agent_lost")).toBeVisible();
  await expect(
    page.getByRole("link", {
      name: `Open execution run ${finishedRun.id}`,
      exact: true,
    }),
  ).toHaveAttribute("href", `/runs?run=${finishedRun.id}`);
  await expect(
    page.getByRole("link", {
      name: `Open retry run ${finishedRun.id}`,
      exact: true,
    }),
  ).toHaveAttribute("href", `/runs?run=${finishedRun.id}`);
});

test("separates fleet policy, observations, and current activity", async ({
  page,
}) => {
  await installMockAPI(page, {
    agents: [
      {
        name: "design-mac",
        type: "agent",
        location: "local",
        labels: { os: "darwin", arch: "arm64" },
        capabilities: ["arch=arm64", "os=darwin"],
        last_seen: new Date().toISOString(),
        status: "busy",
        active_jobs: [finishedRun.id],
        active_slots: 2,
        max_concurrent: 4,
        base_priority: 20,
        priority_ceiling: 80,
        budget: { cores: 6, memory_bytes: 12 * 1024 ** 3 },
        headroom: {
          cores: 3,
          memory_bytes: 6 * 1024 ** 3,
          queue_depth: 1,
          observed_at: new Date().toISOString(),
        },
      },
      {
        name: "old-pool",
        type: "pool",
        location: "unknown",
        labels: {},
        last_seen: new Date().toISOString(),
        status: "idle",
        active_jobs: [],
        max_concurrent: 0,
      },
    ],
  });

  await page.goto("/cluster");
  await expect(
    page.getByRole("heading", { name: "Fleet", exact: true, level: 1 }),
  ).toBeVisible();

  await page.getByRole("button", { name: /design-mac/ }).click();
  const configured = page.getByRole("region", { name: "Configured policy" });
  const observed = page.getByRole("region", { name: "Observed liveness" });
  const activity = page.getByRole("region", { name: "Current activity" });
  await expect(configured.getByText("local", { exact: true })).toBeVisible();
  await expect(configured.getByText("20 (ceiling 80)")).toBeVisible();
  await expect(observed.getByText("3 cores / 6.0 GiB")).toBeVisible();
  await expect(observed.getByText("headroom observed", { exact: true })).toBeVisible();
  await expect(observed.getByText(/controller accepted this headroom/)).toBeVisible();
  await expect(activity.getByText("2", { exact: true })).toBeVisible();
  await expect(
    activity.getByRole("link", { name: finishedRun.id, exact: true }),
  ).toBeVisible();

  await page.getByRole("button", { name: /old-pool/ }).click();
  await expect(
    page.getByText(
      "Configuration unavailable. This executor was inferred from recent activity.",
    ),
  ).toBeVisible();
  await expect(page.getByText("not reported", { exact: true })).toBeVisible();
});

test("keeps every public dashboard navigation target routable", async ({
  page,
}) => {
  await installMockAPI(page);
  const routes = [
    ["Home", "Overview"],
    ["Queue", "Admission queue"],
    ["Crons", "Crons"],
    ["Capacity", "Capacity"],
    ["Fleet", "Fleet"],
    ["Analytics (preview)", "Analytics"],
  ] as const;
  await page.goto("/");
  await page.getByRole("link", { name: "Runs", exact: true }).click();
  await expect(page).toHaveURL(/\/runs$/);
  await expect(page.getByRole("button", { name: "Activity", exact: true })).toBeVisible();
  for (const [link, heading] of routes) {
    await page.getByRole("link", { name: link, exact: true }).click();
    await expect(
      page.getByRole("heading", { name: heading, exact: true, level: 1 }),
    ).toBeVisible();
  }
  const docs = page.getByRole("link", { name: "Docs", exact: true });
  await expect(docs).toHaveAttribute("href", "https://sparkwing.dev/docs/");
  await expect(docs).toHaveAttribute("target", "_blank");
  await expect(page.getByRole("button", { name: "Log out", exact: true })).toHaveCount(0);
});

test("promises no analytics section the product does not have", async ({
  page,
}) => {
  await installMockAPI(page);
  await page.goto("/analytics");
  await expect(
    page.getByRole("heading", { name: "Analytics", exact: true, level: 1 }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Trends", exact: true, level: 2 }),
  ).toBeVisible();
  await expect(page.getByText(/Coming soon/i)).toHaveCount(0);
});

test("allocates collision-free ports for parallel static servers", async () => {
  const [first, second] = await Promise.all([
    startStaticDashboard(),
    startStaticDashboard(),
  ]);
  try {
    expect(new URL(first.origin).port).not.toBe(new URL(second.origin).port);
    const responses = await Promise.all([fetch(first.origin), fetch(second.origin)]);
    expect(responses.every((response) => response.ok)).toBe(true);
  } finally {
    await Promise.all([first.close(), second.close()]);
  }
});

test("closes promptly with an active SSE response", async () => {
  let opened: (() => void) | undefined;
  const responseOpened = new Promise<void>((resolve) => {
    opened = resolve;
  });
  const dashboard = await startStaticDashboard((request, response) => {
    if (request.url !== "/never-ending-events") return false;
    response.writeHead(200, {
      "Cache-Control": "no-cache",
      "Content-Type": "text/event-stream",
    });
    response.write("event: heartbeat\ndata: {}\n\n");
    opened?.();
    return true;
  });
  const controller = new AbortController();
  let closePromise: Promise<void> | undefined;
  try {
    const response = await fetch(`${dashboard.origin}/never-ending-events`, {
      signal: controller.signal,
    });
    const bodyFinished = response.text().then(
      () => "completed",
      () => "terminated",
    );
    await responseOpened;

    closePromise = dashboard.close();
    const outcome = await Promise.race([
      closePromise.then(() => "closed"),
      new Promise<"hung">((resolve) =>
        setTimeout(() => resolve("hung"), 300),
      ),
    ]);
    if (outcome === "hung") controller.abort();
    await closePromise;
    expect(outcome).toBe("closed");
    expect(await bodyFinished).toBe("terminated");
  } finally {
    controller.abort();
    await (closePromise ?? dashboard.close());
  }
});

test("sends cancel and retry actions for an active run", async ({ page }) => {
  const requests: string[] = [];
  const runningDetail = {
    ...finishedDetail,
    run: runningRun,
    nodes: finishedDetail.nodes.map((node) => ({
      ...node,
      status: "running",
      outcome: "",
      finished_at: undefined,
    })),
  };
  await installMockAPI(page, {
    runs: [runningRun],
    details: { [runningRun.id]: runningDetail },
    onRequest: (route) => {
      if (route.request().method() === "POST") requests.push(route.request().url());
    },
  });
  await page.goto(`/runs?run=${runningRun.id}`);

  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await page.getByRole("button", { name: "Confirm cancel" }).click();
  await expect
    .poll(() =>
      requests.some((url) => url.endsWith(`/runs/${runningRun.id}/cancel`)),
    )
    .toBe(true);

  await page.getByRole("button", { name: /^Rerun/ }).last().click();
  await page.getByRole("menuitem", { name: /Rerun from failed/ }).click();
  await expect
    .poll(() =>
      requests.some((url) => url.endsWith(`/runs/${runningRun.id}/retry`)),
    )
    .toBe(true);
});

test("renders the crons overview with its health banner", async ({ page }) => {
  await installMockAPI(page, { crons: cronsOverview });
  await page.goto("/crons");

  await expect(
    page.getByRole("heading", { name: "Crons", exact: true, level: 1 }),
  ).toBeVisible();
  const banner = page.getByRole("region", { name: "Cron health" });
  await expect(
    banner.getByText("1 schedule armed on this host", { exact: true }),
  ).toBeVisible();
  await expect(
    banner.getByText("/home/me/.config/systemd/user/sparkwing-crons.timer"),
  ).toBeVisible();

  await expect(page.locator("[data-schedule-id]")).toHaveCount(2);
  await expect(
    page.locator(`[data-schedule-id="${armedCron.id}"]`).getByText(/^in \d+h/),
  ).toBeVisible();

  const toggle = page.getByRole("button", { name: /show undeclared \(1\)/ });
  await expect(toggle).toBeVisible();
  await toggle.click();
  await expect(page.locator("[data-schedule-id]")).toHaveCount(3);
});

test("warns when the cron tick has gone stale", async ({ page }) => {
  await installMockAPI(page, {
    crons: {
      health: { ...healthyCronHealth, tick_stale: true },
      schedules: [armedCron],
    },
  });
  await page.goto("/crons");

  const banner = page.getByRole("region", { name: "Cron health" });
  await expect(banner.getByText(/Cron ticks have stopped/)).toBeVisible();
  await expect(banner.getByText(/sparkwing crons install/)).toBeVisible();
});

test("names what a locked schedule runs and warns when the checkout has moved past it", async ({
  page,
}) => {
  await installMockAPI(page, {
    crons: {
      health: {
        ...healthyCronHealth,
        armed: 1,
        paused: 0,
        undeclared: 0,
        schedules: 1,
        locked: 1,
        following: 0,
        ahead: 1,
        remedy:
          "1 schedule(s) are pinned behind the checkout; re-run `sparkwing crons install` to pin the checkout as it stands",
      },
      schedules: [aheadCron],
    },
  });
  await page.goto("/crons");

  const row = page.locator(`[data-schedule-id="${aheadCron.id}"]`);
  await expect(row.getByText("ahead", { exact: true })).toBeVisible();
  await expect(row.getByText("abc1234", { exact: true })).toBeVisible();

  const banner = page.getByRole("region", { name: "Cron health" });
  await expect(banner.getByText(/moved past a pin/)).toBeVisible();
  await expect(banner.getByText(/pin the checkout as it stands/)).toBeVisible();
});

test("marks an overridden schedule and shows the declaration beside it", async ({
  page,
}) => {
  await installMockAPI(page, {
    crons: {
      health: {
        ...healthyCronHealth,
        armed: 1,
        paused: 0,
        undeclared: 0,
        schedules: 1,
        stale_override: 1,
      },
      schedules: [overriddenCron],
    },
    cronDetail: {
      schedule: overriddenCron,
      fires: [
        {
          id: "crf_11",
          schedule_id: overriddenCron.id,
          due_at: isoFromNow(-19 * 3_600_000),
          decided_at: isoFromNow(-19 * 3_600_000 + 700),
          outcome: "fired",
          run_id: "run_ovr",
          detail: "",
          args: { depth: "deep", "dry-run": "true" },
          run_status: "passed",
        },
      ],
      upcoming: [isoFromNow(6 * 3_600_000)],
    },
  });
  await page.goto(`/crons?schedule=${overriddenCron.id}`);

  const row = page.locator(`[data-schedule-id="${overriddenCron.id}"]`);
  await expect(row.getByText("override", { exact: true })).toBeVisible();
  await expect(row.getByText("0 5 * * *", { exact: true })).toBeVisible();

  const pane = page.getByRole("region", { name: "Schedule detail" });
  const cron = pane.locator('[data-cadence-field="cron"]');
  await expect(cron.getByText("0 3 * * *", { exact: true })).toBeVisible();
  await expect(cron.getByText("0 5 * * *", { exact: true })).toBeVisible();
  await expect(
    pane.locator('[data-cadence-field="tz"]').getByText("America/Denver", {
      exact: true,
    }),
  ).toBeVisible();
  await expect(pane.getByText(/still applies/)).toBeVisible();
  await expect(
    pane.getByText("--depth=deep --dry-run=true", { exact: true }),
  ).toBeVisible();

  const banner = page.getByRole("region", { name: "Cron health" });
  await expect(banner.getByText(/dotfiles\/vault-sweep/)).toBeVisible();
  await expect(
    banner.getByText(/declaration that has changed/),
  ).toBeVisible();
});

test("pauses a schedule and flips the row it acted on", async ({ page }) => {
  const posted: string[] = [];
  let paused = false;
  await installMockAPI(page, {
    crons: () => ({
      health: healthyCronHealth,
      schedules: [
        paused ? { ...armedCron, paused: true, state: "paused" } : armedCron,
      ],
    }),
    onCronAction: (id, action) => {
      posted.push(`${id}/${action}`);
      if (action === "pause") paused = true;
    },
  });
  await page.goto("/crons");

  const row = page.locator(`[data-schedule-id="${armedCron.id}"]`);
  await row.getByRole("button", { name: "Pause", exact: true }).click();
  await expect.poll(() => posted).toContain(`${armedCron.id}/pause`);
  await expect(
    row.getByRole("button", { name: "Resume", exact: true }),
  ).toBeVisible();
  await expect(row.getByText("paused", { exact: true })).toBeVisible();
});

test("deep-links a schedule and links its fires back to runs", async ({
  page,
}) => {
  await installMockAPI(page, { crons: cronsOverview, cronDetail });
  await page.goto(`/crons?schedule=${armedCron.id}`);

  const pane = page.getByRole("region", { name: "Schedule detail" });
  await expect(pane.getByText(armedCron.name, { exact: true })).toBeVisible();
  await expect(pane.getByText("skip if running", { exact: true })).toBeVisible();
  await expect(pane.getByText("1h", { exact: true })).toBeVisible();
  await expect(
    pane.getByText("previous run still holding admission"),
  ).toBeVisible();
  await expect(pane.getByText("passed", { exact: true })).toBeVisible();
  await expect(
    pane.getByRole("link", { name: "run_abc", exact: true }).first(),
  ).toHaveAttribute("href", "/runs?run=run_abc");
});

test("explains how to declare a schedule when the host has none", async ({
  page,
}) => {
  await installMockAPI(page);
  await page.goto("/crons");

  await expect(
    page.getByRole("heading", { name: "Crons", exact: true, level: 1 }),
  ).toBeVisible();
  await expect(
    page.getByText("No schedules are armed on this host.", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText("on: schedule:", { exact: true })).toBeVisible();
  await expect(
    page.getByText("sparkwing crons install", { exact: true }),
  ).toBeVisible();
  await expect(page.locator("[data-schedule-id]")).toHaveCount(0);
});


test("cron history shows run results, hover details and run links", async ({ page }) => {
  const fires = [
    { ...cronDetail.fires[0], id: "newest", run_status: "success" },
    { ...cronDetail.fires[0], id: "failure", due_at: isoFromNow(-12 * 3_600_000), run_id: "failed_run", run_status: "failed" },
    cronDetail.fires[1],
  ];
  await installMockAPI(page, { crons: { ...cronsOverview, schedules: [armedCron] }, cronDetail: { ...cronDetail, fires } });
  await page.goto("/crons");
  const bars = page.locator("[data-fire-id]");
  await expect(bars).toHaveCount(3);
  await expect(bars.nth(0)).toHaveAttribute("data-fire-id", "crf_01");
  await expect(bars.nth(1)).toHaveClass(/bg-red-400/);
  await expect(bars.nth(2)).toHaveClass(/bg-green-400/);
  await bars.nth(1).focus();
  await expect(page.getByText("Status: failed", { exact: true })).toBeVisible();
  await bars.nth(1).blur();
  await bars.nth(1).hover();
  await expect(page.getByText("Status: failed", { exact: true })).toBeVisible();
  await expect(page.getByText("Outcome: fired", { exact: true })).toBeVisible();
  await bars.nth(0).click();
  await expect(page.getByRole("region", { name: "Schedule detail" })).toBeVisible();
  await bars.nth(1).click();
  await expect(page).toHaveURL(/\/runs\?run=failed_run$/);
});

test("cron history distinguishes empty and unavailable history", async ({ page }) => {
  await installMockAPI(page, { crons: { ...cronsOverview, schedules: [armedCron] }, cronDetail: { ...cronDetail, fires: [] } });
  await page.goto("/crons");
  await expect(page.getByText("No fires yet", { exact: true })).toBeVisible();
  await page.route("**/api/v1/crons/*", route => route.fulfill({ status: 503, body: "unavailable" }));
  await page.reload();
  await expect(page.getByText("History unavailable", { exact: true })).toBeVisible();
});


test("cron detail deep links survive hidden and missing schedules", async ({ page }) => {
  await installMockAPI(page, { crons: cronsOverview, cronDetail: { ...cronDetail, schedule: undeclaredCron } });
  await page.goto(`/crons?schedule=${undeclaredCron.id}`);
  await expect(page.getByRole("region", { name: "Schedule detail" }).getByRole("heading", { name: undeclaredCron.name, exact: true })).toBeVisible();
  await page.route("**/api/v1/crons/missing", route => route.fulfill({ status: 404 }));
  await page.goto("/crons?schedule=missing");
  await expect(page.getByText("That schedule is not on this host.")).toBeVisible();
});


test("runs trigger filters persist and offer badge include and exclude", async ({ page }) => {
  const scheduled = { ...runningRun, trigger_source: "schedule" };
  await installMockAPI(page, { runs: [finishedRun, scheduled] });
  await page.goto("/runs");
  const githubRow = page.locator(`[data-run-id="${finishedRun.id}"]`);
  const scheduledRow = page.locator(`[data-run-id="${scheduled.id}"]`);
  await expect(page.getByRole("button", { name: /^TRIGGER/ })).toBeVisible();
  await githubRow.getByText("github", { exact: true }).click();
  await page.getByRole("button", { name: "+ filter to github", exact: true }).click();
  await expect(page).toHaveURL(/(?:\?|&)trigger=github(?:&|$)/);
  await expect(scheduledRow).toHaveCount(0);
  await page.reload();
  await expect(githubRow).toBeVisible();
  await expect(scheduledRow).toHaveCount(0);
  await githubRow.getByText("github", { exact: true }).click();
  await page.getByRole("button", { name: /exclude github/, exact: false }).click();
  await expect(page).toHaveURL(/(?:\?|&)ntrigger=github(?:&|$)/);
  await expect(githubRow).toHaveCount(0);
  await expect(scheduledRow).toBeVisible();
  await page.goto("/runs?view=pipelines&trigger=schedule");
  await expect(page.getByRole("heading", { name: "Pipelines", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /pre-commit/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /deploy-production/ })).toHaveCount(0);
});

for (const count of [999, 1000]) {
  test(`overview marks capped history at ${count} runs`, async ({ page }) => {
    await installMockAPI(page, {
      runs: Array.from({ length: count }, (_, index) => ({
        ...finishedRun,
        id: `run-${index}`,
        started_at: isoFromNow(-86_400_000),
        finished_at: isoFromNow(-86_340_000),
      })),
    });
    await page.goto("/");
    await expect(page.getByText("Loading...")).toHaveCount(0);
    const notice = page.getByText(
      "Metrics use the latest 1000 runs and may exclude older history.",
    );
    if (count === 1000) {
      await expect(notice).toBeVisible();
    } else {
      await expect(notice).toHaveCount(0);
    }
  });
}
