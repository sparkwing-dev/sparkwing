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

// The fixtures are what the controller encodes for GET /api/v1/runs/{run};
// internal/controllerwire's TestConsoleRunDetailFixturesMatchTheWireType keeps them exact.
function fixture(name: string): import("./api").RunDetailWire {
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

  it("maps jobs, their errors and attempts onto the page's nodes", () => {
    const { run, nodes } = api.runDetailView(fixture("run-detail-jobs.json"));
    assert.equal(api.planFailureHint(run, nodes), null);
    const [build, test] = nodes;
    assert.equal(build.id, "build");
    assert.equal(build.status, "failed");
    assert.match(build.error ?? "", /cannot use \*\*\* as int value/);
    assert.equal(build.execution_attempts, undefined);
    assert.equal(test.status, "success");
    assert.equal(test.duration_ms, 33_000);
    assert.deepEqual(test.annotations, ["42 tests"]);
    assert.equal(test.execution_attempts?.length, 1);
    assert.equal(test.execution_attempts?.[0].executor_name, "cloud");
  });

  it("reads the run detail route without the removed include parameter", async () => {
    const runtime = globalThis as unknown as { fetch: typeof fetch };
    const previousFetch = runtime.fetch;
    const requested: string[] = [];
    runtime.fetch = async (input) => {
      requested.push(String(input));
      return new Response(
        readFileSync(
          join(__dirname, "testdata", "run-detail-plan-failure.json"),
        ),
        { status: 200 },
      );
    };
    try {
      const detail = await api.getRun("run-plan");
      assert.equal(detail?.run.error?.includes("qaDeliberateUndefined"), true);
    } finally {
      runtime.fetch = previousFetch;
    }
    assert.equal(new URL(requested[0], "https://dashboard.example").search, "");
  });
});
