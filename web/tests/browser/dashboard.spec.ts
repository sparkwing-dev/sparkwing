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

type MockAPIOptions = {
  runs?: Record<string, unknown>[];
  details?: Record<string, Record<string, unknown>>;
  unauthorized?: boolean;
  failPath?: string;
  onDetail?: (route: Route, runID: string) => Promise<boolean>;
  onEventStream?: (route: Route) => Promise<void>;
  onLogStream?: (route: Route) => Promise<void>;
  onRequest?: (route: Route) => void;
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
    if (request.method() === "GET" && path === "/api/v1/pipelines") {
      await route.fulfill({ json: { pipelines: {} } });
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
  const newerFinished = deferred();
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
      await route.fulfill({ json: finishedDetail });
      return true;
    },
  });
  await page.goto("/runs");
  await page.locator(`[data-run-id="${finishedRun.id}"]`).click();
  await olderStarted.promise;
  await page.locator(`[data-run-id="${runningRun.id}"]`).click();
  if (!newerDetail) await newerFinished.promise;

  return async () => {
    const olderResponse = page.waitForResponse((response) =>
      new URL(response.url()).pathname.endsWith(`/runs/${finishedRun.id}`),
    );
    olderRelease.resolve();
    await olderResponse;
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

test("keeps every public dashboard navigation target routable", async ({
  page,
}) => {
  await installMockAPI(page);
  const routes = [
    ["Home", "Overview"],
    ["Queue", "Admission queue"],
    ["Capacity", "Capacity"],
    ["Cluster", "Cluster"],
    ["Analytics (preview)", "Analytics"],
  ] as const;
  await page.goto("/");
  await page.getByRole("link", { name: "Runs", exact: true }).click();
  await expect(page).toHaveURL(/\/runs$/);
  await expect(page.getByRole("button", { name: "Activity", exact: true })).toBeVisible();
  for (const [link, heading] of routes) {
    await page.getByRole("link", { name: link, exact: true }).click();
    await expect(page.getByRole("heading", { name: heading, exact: true })).toBeVisible();
  }
  const docs = page.getByRole("link", { name: "Docs", exact: true });
  await expect(docs).toHaveAttribute("href", "https://sparkwing.dev/docs/");
  await expect(docs).toHaveAttribute("target", "_blank");
  await expect(page.getByRole("button", { name: "Log out", exact: true })).toHaveCount(0);
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
