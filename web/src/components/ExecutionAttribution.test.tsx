import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { renderToStaticMarkup } from "react-dom/server";
import { ExecutionAttributionPanel, ExecutionBadge } from "./ExecutionAttribution";

describe("node row execution mark", () => {
  it("renders only a focusable icon and tooltip, without a location pill", () => {
    const html = renderToStaticMarkup(
      <ExecutionBadge node={{
        id: "verify-production-artifacts-on-moonborn",
        status: "done",
        outcome: "success",
        deps: [],
        duration_ms: 100,
        executor_kind: "agent",
        executor_name: "moonborn",
        executor_location: "local",
      }} />,
    );
    assert.match(html, /tabindex="0"/i);
    assert.match(html, /aria-label="Ran on moonborn \(your machine\)"/);
    assert.match(html, /<svg/);
    assert.doesNotMatch(html, />Local</);
  });
});

describe("execution history", () => {
  it("shows an executor supplied for a historical attempt", () => {
    const html = renderToStaticMarkup(<ExecutionAttributionPanel node={{
      id: "build", status: "done", outcome: "success", deps: [], duration_ms: 10,
      execution_attempts: [{ run_id: "run-1", attempt: 1, started_at: "2026-09-23T00:00:00Z", location: "local", execution_site: "machine", execution_site_name: "moonborn" }],
    }} />);
    assert.match(html, /agent moonborn|machine moonborn/);
    assert.doesNotMatch(html, /Executor unknown/);
  });
});
