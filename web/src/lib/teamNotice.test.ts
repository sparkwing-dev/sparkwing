import assert from "node:assert/strict";
import { afterEach, describe, it } from "node:test";
import {
  createdNotice,
  joinedNotice,
  rememberTeamNotice,
  switchedNotice,
  takeTeamNotice,
} from "./teamNotice";

const runtime = globalThis as unknown as {
  window?: { sessionStorage: Storage };
};

function fakeStorage(): Storage {
  const data = new Map<string, string>();
  return {
    get length() {
      return data.size;
    },
    clear: () => data.clear(),
    getItem: (k) => data.get(k) ?? null,
    key: (i) => [...data.keys()][i] ?? null,
    removeItem: (k) => void data.delete(k),
    setItem: (k, v) => void data.set(k, String(v)),
  };
}

afterEach(() => {
  delete runtime.window;
});

describe("team notices", () => {
  it("tell the reader their other teams are kept", () => {
    for (const message of [
      switchedNotice("Acme"),
      joinedNotice("Acme"),
      createdNotice("Acme"),
    ]) {
      assert.match(message, /Acme/);
      assert.match(message, /other teams are still in the team menu/);
      assert.ok(!message.includes(String.fromCharCode(0x2014)));
    }
  });

  it("survive one reload and are shown once", () => {
    runtime.window = { sessionStorage: fakeStorage() };
    rememberTeamNotice(switchedNotice("Acme"));
    assert.equal(takeTeamNotice(), switchedNotice("Acme"));
    assert.equal(takeTeamNotice(), "");
  });

  it("are skipped, not thrown, when storage is refused", () => {
    runtime.window = {
      get sessionStorage(): Storage {
        throw new Error("blocked");
      },
    };
    assert.doesNotThrow(() => rememberTeamNotice("x"));
    assert.equal(takeTeamNotice(), "");
  });
});
