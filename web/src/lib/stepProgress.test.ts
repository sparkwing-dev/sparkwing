import { describe, it } from "node:test";
import assert from "node:assert/strict";
import {
  STEP_STATE_COLOR,
  layoutStepProgress,
  stepIndexAt,
  stepProgressLabel,
  stepState,
  type StepState,
} from "./stepProgress";

describe("stepState", () => {
  it("maps wire statuses and treats a missing status as pending", () => {
    assert.equal(stepState("passed"), "succeeded");
    assert.equal(stepState("running"), "running");
    assert.equal(stepState(undefined), "pending");
  });
});

describe("STEP_STATE_COLOR", () => {
  it("shows cancelled in the console's amber and keeps skipped muted", () => {
    assert.equal(STEP_STATE_COLOR.cancelled, "rgba(251,191,36,0.95)");
    assert.notEqual(STEP_STATE_COLOR.skipped, STEP_STATE_COLOR.cancelled);
  });
});

describe("stepProgressLabel", () => {
  it("counts succeeded steps against the total and names the rest", () => {
    const states: StepState[] = [
      ...Array<StepState>(14).fill("succeeded"),
      "running",
      ...Array<StepState>(7).fill("pending"),
    ];
    assert.equal(
      stepProgressLabel(states),
      "14 of 22 steps succeeded, 1 running, 7 pending",
    );
  });

  it("reads naturally for a single step", () => {
    assert.equal(
      stepProgressLabel(["failed"]),
      "0 of 1 step succeeded, 1 failed",
    );
  });
});

describe("layoutStepProgress", () => {
  it("draws one segment per step in step order within the width", () => {
    const states: StepState[] = [
      "succeeded",
      "succeeded",
      "running",
      "pending",
    ];
    const layout = layoutStepProgress(states, 103);
    assert.equal(layout.mode, "segments");
    assert.deepEqual(
      layout.rects.map((r) => r.state),
      states,
    );
    const last = layout.rects[layout.rects.length - 1];
    assert.ok(Math.abs(last.x + last.w - 103) < 1e-9);
    assert.equal(stepIndexAt(layout, 60, states.length), 2);
  });

  it("keeps a single step as one full-width segment", () => {
    const layout = layoutStepProgress(["running"], 120);
    assert.equal(layout.rects.length, 1);
    assert.equal(layout.rects[0].w, 120);
  });

  it("switches to proportional fills in step order when segments get too thin", () => {
    const states: StepState[] = [
      ...Array<StepState>(30).fill("succeeded"),
      ...Array<StepState>(10).fill("pending"),
      ...Array<StepState>(20).fill("succeeded"),
    ];
    const layout = layoutStepProgress(states, 120);
    assert.equal(layout.mode, "proportional");
    assert.deepEqual(
      layout.rects.map((r) => [r.state, r.count]),
      [
        ["succeeded", 30],
        ["pending", 10],
        ["succeeded", 20],
      ],
    );
    const last = layout.rects[layout.rects.length - 1];
    assert.ok(Math.abs(last.x + last.w - 120) < 1e-9);
    assert.equal(stepIndexAt(layout, 119.9, states.length), 59);
  });
});
