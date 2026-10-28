import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { navTabs } from "./navTabs";

const hrefs = (status: Parameters<typeof navTabs>[0]) =>
  navTabs(status).map((tab) => tab.href);

describe("navTabs", () => {
  it("links local secrets only on a controller without teams", () => {
    assert.ok(hrefs("single-team").includes("/secrets"));
    assert.equal(hrefs("ready").includes("/secrets"), false);
    assert.equal(hrefs("loading").includes("/secrets"), false);
  });

  it("links no team views for a waitlisted account", () => {
    assert.deepEqual(hrefs("waitlisted"), []);
  });

  it("keeps team secrets under the Team tab for a signed-in member", () => {
    assert.ok(hrefs("ready").includes("/team"));
    assert.equal(hrefs("single-team").includes("/team"), false);
  });

  it("links Queue and Capacity only on the local dashboard", () => {
    assert.ok(hrefs("single-team").includes("/queue"));
    assert.ok(hrefs("single-team").includes("/capacity"));
    assert.equal(hrefs("ready").includes("/queue"), false);
    const runtime = globalThis as { window?: unknown };
    runtime.window = { __SPARKWING_REQUIRE_LOGIN__: "true" };
    try {
      assert.equal(hrefs("single-team").includes("/queue"), false);
      assert.equal(hrefs("single-team").includes("/capacity"), false);
      assert.ok(hrefs("single-team").includes("/secrets"));
    } finally {
      delete runtime.window;
    }
  });
});
