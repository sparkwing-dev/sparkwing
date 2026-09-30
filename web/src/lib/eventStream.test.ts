import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { parseEventBlock } from "./eventStream";
import { signInCodeFrom } from "./localSession";

describe("parseEventBlock", () => {
  it("reads a named event and joins its data lines", () => {
    assert.deepEqual(parseEventBlock("event: node_started\ndata: {\"a\":1}\ndata: more"), {
      type: "node_started",
      data: "{\"a\":1}\nmore",
    });
  });
  it("defaults to a message and skips comments", () => {
    assert.deepEqual(parseEventBlock(": keepalive\ndata:line"), { type: "message", data: "line" });
  });
  it("ignores a block with no data", () => {
    assert.equal(parseEventBlock(": keepalive"), null);
  });
});

describe("signInCodeFrom", () => {
  it("reads the code from the fragment only", () => {
    assert.equal(signInCodeFrom("#code=abc%2Fdef"), "abc/def");
    assert.equal(signInCodeFrom("#x=1&code=zz"), "zz");
    assert.equal(signInCodeFrom(""), null);
    assert.equal(signInCodeFrom("#codex=1"), null);
  });
});
