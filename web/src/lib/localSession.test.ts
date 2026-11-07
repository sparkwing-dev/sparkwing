import assert from "node:assert/strict";
import { afterEach, describe, it } from "node:test";

import { cancelRun } from "./api";
import { codeFromFragment, ensureLocalSignIn, localSession, localSignedIn, logOutLocal } from "./localSession";

type FakeWindow = {
  __SPARKWING_REQUIRE_LOGIN__?: string;
  localStorage: Storage;
  location: { hash: string; pathname: string; search: string };
  history: { replaceState: (data: unknown, unused: string, url?: string) => void };
};

function memoryStorage(): Storage {
  const items = new Map<string, string>();
  return {
    get length() {
      return items.size;
    },
    clear: () => items.clear(),
    getItem: (key) => items.get(key) ?? null,
    key: (index) => [...items.keys()][index] ?? null,
    removeItem: (key) => void items.delete(key),
    setItem: (key, value) => void items.set(key, value),
  };
}

const runtime = globalThis as unknown as { window?: FakeWindow; fetch: typeof fetch };
const previousFetch = runtime.fetch;

function page(hash: string, mode = "false"): { w: FakeWindow; replaced: string[] } {
  const replaced: string[] = [];
  const w: FakeWindow = {
    __SPARKWING_REQUIRE_LOGIN__: mode,
    localStorage: memoryStorage(),
    location: { hash, pathname: "/runs", search: "?x=1" },
    history: {
      replaceState: (_data, _unused, url) => {
        replaced.push(url ?? "");
        w.location.hash = "";
      },
    },
  };
  runtime.window = w;
  return { w, replaced };
}

afterEach(() => {
  delete runtime.window;
  runtime.fetch = previousFetch;
});

describe("codeFromFragment", () => {
  it("reads the sign-in code from the fragment only", () => {
    assert.equal(codeFromFragment("#code=abc%2Fdef"), "abc/def");
    assert.equal(codeFromFragment("#x=1&code=zz"), "zz");
    assert.equal(codeFromFragment(""), null);
    assert.equal(codeFromFragment("#codex=1"), null);
  });
});

describe("local sign-in", () => {
  it("drops the code from history, exchanges it and sends the token as a bearer", async () => {
    const { w, replaced } = page("#code=one-time");
    const calls: { url: string; body?: string; auth: string | null; cookie: string | null }[] = [];
    runtime.fetch = async (input, init) => {
      const headers = new Headers(init?.headers);
      calls.push({ url: String(input), body: init?.body as string | undefined, auth: headers.get("Authorization"), cookie: headers.get("Cookie") });
      if (String(input) === "/login") {
        assert.deepEqual(replaced, ["/runs?x=1"], "the code left history before the exchange");
        return Response.json({ token: "session-one" });
      }
      return new Response(null, { status: 204 });
    };
    await cancelRun("run-one");
    assert.equal(w.location.hash, "");
    assert.equal(calls[0].url, "/login");
    assert.equal(calls[0].body, JSON.stringify({ code: "one-time" }));
    assert.equal(w.localStorage.getItem("sparkwing.localSession"), "session-one");
    assert.equal(calls[1].auth, "Bearer session-one");
    assert.equal(calls[1].cookie, null);
    assert.equal(localSignedIn(), true);
  });

  it("keeps no token when the exchange is refused", async () => {
    const { w } = page("#code=spent");
    runtime.fetch = async () => new Response(null, { status: 401 });
    await (ensureLocalSignIn() ?? Promise.resolve());
    assert.equal(w.localStorage.getItem("sparkwing.localSession"), null);
    assert.equal(localSession(), null);
  });

  it("ignores a fragment outside the local dashboard", async () => {
    const { replaced } = page("#code=planted", "true");
    let calls = 0;
    runtime.fetch = async () => {
      calls++;
      return new Response(null, { status: 204 });
    };
    await (ensureLocalSignIn() ?? Promise.resolve());
    assert.equal(calls, 0);
    assert.deepEqual(replaced, []);
    assert.equal(localSession(), null);
  });

  it("forgets the token only after the controller revokes it", async () => {
    const { w } = page("");
    w.localStorage.setItem("sparkwing.localSession", "session-two");
    const calls: { url: string; method?: string; auth: string | null }[] = [];
    runtime.fetch = async (input, init) => {
      calls.push({ url: String(input), method: init?.method, auth: new Headers(init?.headers).get("Authorization") });
      return new Response(null, { status: 503 });
    };
    await assert.rejects(logOutLocal(), /still active/);
    assert.equal(w.localStorage.getItem("sparkwing.localSession"), "session-two");
    runtime.fetch = async () => new Response(null, { status: 204 });
    await logOutLocal();
    assert.deepEqual(calls, [{ url: "/logout", method: "POST", auth: "Bearer session-two" }]);
    assert.equal(localSession(), null);
  });
});
