import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { before, describe, it } from "node:test";

type RuntimeWindow = {
  location?: {
    pathname: string;
    search: string;
    assign: (url: string) => void;
  };
};
let api!: typeof import("./api");

before(async () => {
  const runtime = globalThis as unknown as { window?: RuntimeWindow };
  const hadWindow = Object.prototype.hasOwnProperty.call(runtime, "window");
  const previousWindow = runtime.window;
  runtime.window = {};
  try {
    api = await import("./api");
  } finally {
    if (hadWindow) runtime.window = previousWindow;
    else delete runtime.window;
  }
});

// The fixtures are what the controller encodes for GET /api/v1/runs/{run} and
// /steps; internal/controller's TestConsoleFixturesMatchTheWireTypes keeps them exact.
function fixture<T = import("./api").RunDetailWire>(name: string): T {
  return JSON.parse(readFileSync(join(__dirname, "testdata", name), "utf8"));
}

describe("runDetailView", () => {
  it("shows a run that failed to plan with its error and the CLI's hint", () => {
    const { run, nodes } = api.runDetailView(
      fixture("run-detail-plan-failure.json"),
    );
    assert.equal(run.id, "run-plan");
    assert.equal(run.pipeline, "hello");
    assert.equal(run.status, "failed");
    assert.equal(run.repo, "gitlab.com/acme/app");
    assert.equal(run.git_branch, "main");
    assert.equal(run.retry_of, "run-earlier");
    assert.deepEqual(run.args, { target: "prod" });
    assert.match(run.error ?? "", /undefined: qaDeliberateUndefined/);
    assert.deepEqual(nodes, []);
    assert.match(
      api.planFailureHint(run, nodes) ?? "",
      /sparkwing pipeline plan --pipeline hello/,
    );
  });

  it("maps jobs, their needs, errors, attempts and steps onto the page's nodes", () => {
    const { run, nodes } = api.runDetailView(
      fixture("run-detail-jobs.json"),
      fixture<{ steps: import("./api").StepDetailWire[] }>("run-steps.json").steps,
    );
    assert.equal(api.planFailureHint(run, nodes), null);
    assert.equal(run.retry_mode, "failed");
    assert.deepEqual(run.reused_jobs, ["lint"]);
    assert.equal("trigger_source" in run, false);
    const [lint, build, notify, deploy, test] = nodes;
    assert.equal(notify.on_failure_of, "build");
    assert.deepEqual([deploy.groups, deploy.approval, deploy.status], [["release"], true, "pending"]);
    assert.deepEqual(test.groups, ["checks"]);
    assert.deepEqual(lint.deps, []);
    assert.equal(build.id, "build");
    assert.deepEqual(build.deps, ["lint"]);
    assert.equal(build.status, "failed");
    assert.match(build.error ?? "", /cannot use \*\*\* as int value/);
    assert.equal(build.execution_attempts, undefined);
    assert.equal(build.work, undefined);
    assert.equal(test.status, "success");
    assert.equal(test.duration_ms, 33_000);
    assert.deepEqual(test.annotations, ["42 tests"]);
    assert.deepEqual(test.execution_attempts?.[0], {
      run_id: "run-jobs",
      node_id: "test",
      attempt: 1,
      executor_kind: "runner",
      executor_name: "moonborn",
      location: "local",
      started_at: "2026-10-28T05:01:19Z",
      finished_at: "2026-10-28T05:01:52Z",
      outcome: "success",
      failure_reason: undefined,
    });
    assert.deepEqual(
      test.work?.steps?.map((step) => [step.id, step.status, step.duration_ms, step.annotations, step.summary]),
      [["unit", "passed", 33_000, ["42 tests"], "## Coverage\n91%"]],
    );
  });

  it("reads the run detail route without the removed include parameter", async () => {
    const runtime = globalThis as unknown as { fetch: typeof fetch };
    const previousFetch = runtime.fetch;
    const requested: string[] = [];
    runtime.fetch = async (input) => {
      requested.push(String(input));
      const name = String(input).endsWith("/steps")
        ? "run-steps.json"
        : "run-detail-plan-failure.json";
      return new Response(readFileSync(join(__dirname, "testdata", name)), {
        status: 200,
      });
    };
    try {
      const detail = await api.getRun("run-plan");
      assert.equal(detail?.run.error?.includes("qaDeliberateUndefined"), true);
    } finally {
      runtime.fetch = previousFetch;
    }
    assert.deepEqual(
      requested.map((url) => new URL(url, "https://dashboard.example").pathname + new URL(url, "https://dashboard.example").search),
      ["/api/v1/runs/run-plan", "/api/v1/runs/run-plan/steps"],
    );
  });
});
