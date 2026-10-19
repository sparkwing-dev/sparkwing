import assert from "node:assert/strict";
import { afterEach, describe, it } from "node:test";

import { cancelRun } from "./api";
import { localSession, logOutLocal, sessionFromFragment } from "./localSession";

type FakeWindow = {
  __SPARKWING_REQUIRE_LOGIN__?: string;
  sessionStorage: Storage;
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
    sessionStorage: memoryStorage(),
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

describe("sessionFromFragment", () => {
  it("reads the session from the fragment only", () => {
    assert.equal(sessionFromFragment("#s=abc%2Fdef"), "abc/def");
    assert.equal(sessionFromFragment("#x=1&s=zz"), "zz");
    assert.equal(sessionFromFragment(""), null);
    assert.equal(sessionFromFragment("#sx=1"), null);
  });
});

describe("local session adoption", () => {
  it("moves the fragment into sessionStorage, clears it and sends it as a bearer", async () => {
    const { w, replaced } = page("#s=session-one");
    let headers = new Headers();
    runtime.fetch = async (_input, init) => {
      headers = new Headers(init?.headers);
      return new Response(null, { status: 204 });
    };
    await cancelRun("run-one");
    assert.deepEqual(replaced, ["/runs?x=1"]);
    assert.equal(w.location.hash, "");
    assert.equal(w.sessionStorage.getItem("sparkwing.localSession"), "session-one");
    assert.equal(headers.get("Authorization"), "Bearer session-one");
    assert.equal(headers.get("Cookie"), null);
  });

  it("ignores a fragment outside the local dashboard", () => {
    const { w, replaced } = page("#s=planted", "true");
    assert.equal(localSession(), null);
    assert.deepEqual(replaced, []);
    assert.equal(w.sessionStorage.getItem("sparkwing.localSession"), null);
  });

  it("logs out with the bearer and forgets the session", async () => {
    const { w } = page("#s=session-two");
    const calls: { url: string; method?: string; auth: string | null }[] = [];
    runtime.fetch = async (input, init) => {
      calls.push({ url: String(input), method: init?.method, auth: new Headers(init?.headers).get("Authorization") });
      return new Response(null, { status: 204 });
    };
    await logOutLocal();
    assert.deepEqual(calls, [{ url: "/logout", method: "POST", auth: "Bearer session-two" }]);
    assert.equal(w.sessionStorage.getItem("sparkwing.localSession"), null);
    assert.equal(localSession(), null);
  });
});
