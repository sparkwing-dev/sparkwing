import assert from "node:assert/strict";
import { it } from "node:test";
import { renderToStaticMarkup } from "react-dom/server";
import type { Run } from "@/lib/api";
import SetupPanel from "./SetupPanel";

it("shows queued time instead of a zero start in setup, including cancelled runs", () => {
  for (const status of ["pending", "cancelled"]) {
    const run: Run = {
      id: "before-start", pipeline: "build", status,
      started_at: "0001-01-01T00:00:00Z", created_at: "2026-10-30T10:00:00Z",
      finished_at: status === "cancelled" ? "2026-10-30T10:01:00Z" : undefined,
    };
    const html = renderToStaticMarkup(<SetupPanel run={run} collapsed={false} onToggle={() => {}} />);
    assert.match(html, /Not started/);
    assert.match(html, /queued /);
    assert.doesNotMatch(html, /duration|Jan 1, 1/);
  }
});
