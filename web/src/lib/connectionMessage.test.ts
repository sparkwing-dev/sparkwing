import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { connectionMessage } from "./connectionMessage";

describe("connectionMessage", () => {
  it("sends a signed-out local browser to dashboard open", () => {
    const message = connectionMessage("unauthorized", true);
    assert.match(message.hint, /sparkwing dashboard open/);
    assert.equal(message.signIn, undefined);
  });

  it("asks a hosted user to sign in again", () => {
    const message = connectionMessage("unauthorized", false);
    assert.doesNotMatch(message.hint, /dashboard open/);
    assert.equal(message.signIn, true);
  });

  it("reports a permission refusal without suggesting a sign-in", () => {
    for (const local of [true, false]) {
      const message = connectionMessage("forbidden", local);
      assert.match(message.hint, /permission/);
      assert.doesNotMatch(message.hint, /dashboard open|sign in/i);
      assert.equal(message.signIn, undefined);
    }
  });
});
