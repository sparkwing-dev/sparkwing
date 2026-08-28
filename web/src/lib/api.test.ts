import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { getNodeStreamUrl } from "./api";

describe("getNodeStreamUrl", () => {
  it("requests structured events for the live log parser", () => {
    const url = getNodeStreamUrl("run-one", "verify");
    assert.equal(
      url,
      "/api/v1/runs/run-one/logs/verify/stream?format=ndjson",
    );
    assert.doesNotMatch(url, /format=ansi/);
  });
});
