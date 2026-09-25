import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { renderToStaticMarkup } from "react-dom/server";
import RunRail from "./RunRail";

describe("RunRail", () => {
  it("keeps item order, status colors, selected ring, and full keyboard labels", () => {
    const html = renderToStaticMarkup(
      <RunRail
        kind="runs"
        items={[
          { id: "first", label: "repo / build / main / 10:00", dotClass: "bg-green-400" },
          { id: "second", label: "repo / deploy / next / 11:00", dotClass: "bg-red-400" },
        ]}
        selectedID="second"
        onSelect={() => {}}
      />,
    );
    assert.ok(html.indexOf('data-rail-id="first"') < html.indexOf('data-rail-id="second"'));
    assert.match(html, /aria-label="repo \/ build \/ main \/ 10:00"/);
    assert.match(html, /aria-label="repo \/ deploy \/ next \/ 11:00"/);
    assert.match(html, /data-rail-id="first"[^>]*aria-pressed="false"/);
    assert.match(html, /data-rail-id="second"[^>]*aria-pressed="true"/);
    assert.match(html, /bg-green-400/);
    assert.match(html, /bg-red-400/);
    assert.match(html, /ring-2/);
  });
});
