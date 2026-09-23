import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { renderToStaticMarkup } from "react-dom/server";
import { ExecutionBadge } from "./ExecutionAttribution";

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
