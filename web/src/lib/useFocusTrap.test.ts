import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { trapTarget } from "./useFocusTrap";

describe("trapTarget", () => {
  const stops = ["close", "pipeline", "submit"];

  it("wraps Tab from the last control to the first and back", () => {
    assert.equal(trapTarget(stops, "submit", false), "close");
    assert.equal(trapTarget(stops, "close", true), "submit");
  });

  it("leaves moves between inner controls to the browser", () => {
    assert.equal(trapTarget(stops, "close", false), null);
    assert.equal(trapTarget(stops, "submit", true), null);
  });

  it("pulls focus that escaped the trap back to its edge", () => {
    assert.equal(trapTarget(stops, null, false), "close");
    assert.equal(trapTarget(stops, "outside", true), "submit");
  });

  it("has nowhere to send focus without controls", () => {
    assert.equal(trapTarget([], null, false), null);
  });
});
