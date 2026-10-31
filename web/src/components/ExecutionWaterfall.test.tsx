import assert from "node:assert/strict";
import { it } from "node:test";
import { renderToStaticMarkup } from "react-dom/server";
import type { Run } from "@/lib/api";
import ExecutionWaterfall from "./ExecutionWaterfall";

it("omits zero start timestamps from the timing waterfall", () => {
  const run: Run = { id: "pending", pipeline: "build", status: "pending", started_at: "0001-01-01T00:00:00Z" };
  const html = renderToStaticMarkup(<ExecutionWaterfall run={run} nodes={[{
    id: "not-started", status: "pending", outcome: "", deps: [], duration_ms: 0, started_at: run.started_at,
  }]} />);
  assert.match(html, /No node timing data yet/);
});
