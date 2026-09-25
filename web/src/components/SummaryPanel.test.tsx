import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { renderToStaticMarkup } from "react-dom/server";
import type { Node as RunNode, Run } from "@/lib/api";
import SummaryPanel from "./SummaryPanel";

describe("SummaryPanel", () => {
  it("renders a failed step's colored output without raw escapes", () => {
    const run = {
      id: "run-one",
      pipeline: "build",
      status: "failed",
      started_at: "2026-09-23T10:00:00Z",
    } as Run;
    const failed: RunNode = {
      id: "test",
      status: "done",
      outcome: "failed",
      deps: [],
      duration_ms: 100,
      error: "step unit: \x1b[31mFAIL\x1b[0m pkg\n\x1b[1mexit 1\x1b[0m",
    };
    const html = renderToStaticMarkup(
      <SummaryPanel
        run={run}
        nodes={[failed]}
        collapsed={false}
        onToggle={() => {}}
      />,
    );
    assert.doesNotMatch(html, /\x1b/);
    assert.match(html, /<span class="text-red-400">FAIL<\/span> pkg/);
    assert.match(html, /<span class="font-bold">exit 1<\/span>/);
  });
});
