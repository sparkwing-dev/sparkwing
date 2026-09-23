import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { completenessLine, completenessSettled } from "./logCompleteness";

describe("completenessLine", () => {
  it("frames a cut-off log's message", () => {
    assert.equal(
      completenessLine({
        state: "cut_off",
        lines: 1234,
        message:
          "logs cut off: the log stream ended without the runner's confirmation after line 1,234",
      }),
      "— logs cut off: the log stream ended without the runner's confirmation after line 1,234 —",
    );
  });

  it("frames missing lines and an unconfirmed runner", () => {
    assert.equal(
      completenessLine({
        state: "incomplete",
        lines: 9,
        missing_lines: 2,
        message: "logs incomplete: 2 lines missing",
      }),
      "— logs incomplete: 2 lines missing —",
    );
    assert.match(
      completenessLine({
        state: "unconfirmed",
        lines: 3,
        message:
          "logs unconfirmed: this runner does not report whether its log is complete (3 lines stored)",
      }) ?? "",
      /^— logs unconfirmed/,
    );
  });

  it("draws nothing for a whole, live or untracked log", () => {
    assert.equal(completenessLine({ state: "complete", lines: 3 }), null);
    assert.equal(completenessLine({ state: "streaming", lines: 3 }), null);
    assert.equal(completenessLine({ state: "unknown", lines: 0 }), null);
    assert.equal(completenessLine(null), null);
  });
});

describe("completenessSettled", () => {
  it("waits out the seal grace", () => {
    assert.equal(completenessSettled({ state: "streaming", lines: 1 }), false);
    assert.equal(completenessSettled({ state: "cut_off", lines: 1 }), true);
    assert.equal(completenessSettled(null), false);
  });
});
