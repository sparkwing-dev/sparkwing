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

  it("keeps a job's work to its current attempt and earlier steps in labelled history", () => {
    const wire = fixture("run-detail-jobs.json");
    const job = { ...wire.jobs[0], id: "retried", attempt: 2, status: "running", outcome: undefined };
    const step = (attempt: number, id: string, status: string, summary = "") => ({
      job_id: "retried", id, label: "", attempt, status, summary,
      annotations: [{ level: "warning", message: `${id} ${attempt}`, file: "a.go", line: 3 }],
    });
    const { nodes } = api.runDetailView({ ...wire, jobs: [job] }, [
      step(1, "unit", "passed", "OLD PASS"),
      step(1, "setup", "passed"),
      step(2, "setup", "running"),
    ]);
    const [retried] = nodes;
    assert.deepEqual(retried.work?.steps?.map((s) => [s.id, s.status, s.summary]), [["setup", "running", undefined]]);
    assert.deepEqual(retried.work?.steps?.[0].annotations, ["warning: a.go:3: setup 2"]);
    assert.deepEqual(
      retried.step_history?.map((h) => [h.attempt, h.steps.map((s) => s.id)]),
      [[1, ["unit", "setup"]]],
    );
  });

  it("marks steps unavailable instead of showing empty work", () => {
    const detail = api.runDetailView(fixture("run-detail-jobs.json"), null);
    assert.equal(detail.stepsUnavailable, true);
    assert.ok(detail.nodes.every((node) => node.work === undefined));
    assert.equal(api.runDetailView(fixture("run-detail-jobs.json"), []).stepsUnavailable, false);
  });

  it("keeps the run's notices and the job's summary", () => {
    const wire = fixture("run-detail-jobs.json");
    const notice = { kind: "support_ending", version: "v1.2.3", reason: "v1.2.3 support ends soon", hint: "upgrade" };
    const detail = api.runDetailView(
      { ...wire, notices: [notice], jobs: [{ ...wire.jobs[0], summary: "## Deployed" }] },
      [],
    );
    assert.deepEqual(detail.notices, [notice]);
    assert.equal(detail.nodes[0].summary, "## Deployed");
  });

  it("reads a failed or malformed steps answer as unavailable", async () => {
    const runtime = globalThis as unknown as { fetch: typeof fetch };
    const previousFetch = runtime.fetch;
    for (const steps of [new Response("down", { status: 503 }), new Response("{", { status: 200 })]) {
      runtime.fetch = async (input) =>
        String(input).endsWith("/steps")
          ? steps
          : new Response(readFileSync(join(__dirname, "testdata", "run-detail-jobs.json")), { status: 200 });
      try {
        const detail = await api.getRun("run-jobs");
        assert.equal(detail?.stepsUnavailable, true);
        assert.ok((detail?.nodes.length ?? 0) > 0);
      } finally {
        runtime.fetch = previousFetch;
      }
    }
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
