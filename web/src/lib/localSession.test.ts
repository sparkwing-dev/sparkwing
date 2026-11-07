import assert from "node:assert/strict";
import { afterEach, describe, it } from "node:test";

import { localDashboard, localSignedIn, logOutLocal } from "./localSession";

const runtime = globalThis as unknown as {
  window?: { __SPARKWING_REQUIRE_LOGIN__?: string; document?: { cookie?: string } };
  fetch: typeof fetch;
};
const previousFetch = runtime.fetch;

afterEach(() => {
  delete runtime.window;
  runtime.fetch = previousFetch;
});

describe("local session", () => {
  it("recognizes the local dashboard only from its page flag", () => {
    runtime.window = { __SPARKWING_REQUIRE_LOGIN__: "false" };
    assert.equal(localDashboard(), true);
    runtime.window = { __SPARKWING_REQUIRE_LOGIN__: "true" };
    assert.equal(localDashboard(), false);
  });

  it("offers sign-out only while the local marker cookie is present", () => {
    runtime.window = { __SPARKWING_REQUIRE_LOGIN__: "false", document: { cookie: "theme=dark" } };
    assert.equal(localSignedIn(), false);
    runtime.window = { __SPARKWING_REQUIRE_LOGIN__: "false", document: { cookie: "theme=dark; sw_dashboard_signed_in=1" } };
    assert.equal(localSignedIn(), true);
    runtime.window = { __SPARKWING_REQUIRE_LOGIN__: "true", document: { cookie: "sw_dashboard_signed_in=1" } };
    assert.equal(localSignedIn(), false);
  });

  it("logs out with a bare POST that carries no bearer", async () => {
    const calls: { url: string; method?: string; auth: string | null }[] = [];
    runtime.fetch = async (input, init) => {
      calls.push({ url: String(input), method: init?.method, auth: new Headers(init?.headers).get("Authorization") });
      return new Response(null, { status: 204 });
    };
    await logOutLocal();
    assert.deepEqual(calls, [{ url: "/logout", method: "POST", auth: null }]);
  });

  it("rejects when the controller could not revoke the session", async () => {
    runtime.fetch = async () => new Response(null, { status: 503 });
    await assert.rejects(logOutLocal(), /still active/);
    runtime.fetch = async () => new Response(null, { status: 401 });
    await logOutLocal();
  });
});
