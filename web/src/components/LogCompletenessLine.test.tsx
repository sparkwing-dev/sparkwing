import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { renderToStaticMarkup } from "react-dom/server";
import LogCompletenessLine from "./LogCompletenessLine";

describe("LogCompletenessLine", () => {
  it("renders a cut-off log as one status line outside the log text", () => {
    const html = renderToStaticMarkup(
      <LogCompletenessLine
        completeness={{
          state: "cut_off",
          lines: 1234,
          message:
            "logs cut off: the log stream ended without the runner's confirmation after line 1,234",
        }}
      />,
    );
    assert.match(html, /role="status"/);
    assert.match(html, /data-log-completeness="cut_off"/);
    assert.match(html, /select-none/);
    assert.match(html, /\[logs cut off: .* after line 1,234\]/);
  });

  it("renders nothing for a complete log", () => {
    assert.equal(
      renderToStaticMarkup(
        <LogCompletenessLine completeness={{ state: "complete", lines: 3 }} />,
      ),
      "",
    );
  });
});
