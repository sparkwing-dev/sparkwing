import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { teamTabs } from "./TeamShell";

describe("teamTabs", () => {
  it("hides Billing until controller capabilities enable it", () => {
    assert.equal(teamTabs(null).some((tab) => tab.label === "Billing"), false);
    assert.equal(teamTabs({ mode: "cluster", teams: { enabled: true } }).some((tab) => tab.label === "Billing"), false);
    assert.equal(teamTabs({ mode: "cluster", billing: { enabled: false } }).some((tab) => tab.label === "Billing"), false);
    assert.equal(teamTabs({ mode: "cluster", billing: { enabled: true } }).some((tab) => tab.label === "Billing"), true);
  });
});
