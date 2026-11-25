import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, it } from "node:test";
import { startPoll } from "./poll";

const runtime = globalThis as unknown as { document?: { hidden: boolean } };

async function settle() {
  for (let i = 0; i < 5; i++) await Promise.resolve();
}

describe("startPoll", () => {
  beforeEach(() => {
    runtime.document = { hidden: false };
  });
  afterEach(() => {
    delete runtime.document;
  });

  it("skips ticks while a request is in flight and resumes after it answers", async (t) => {
    t.mock.timers.enable({ apis: ["setInterval"] });
    let calls = 0;
    let answer!: () => void;
    const stop = startPoll(() => {
      calls++;
      return new Promise<void>((resolve) => {
        answer = resolve;
      });
    }, 5_000);
    t.after(stop);
    await settle();
    assert.equal(calls, 1);
    t.mock.timers.tick(15_000);
    assert.equal(calls, 1, "a tick must not start a second request over a pending one");
    answer();
    await settle();
    t.mock.timers.tick(5_000);
    assert.equal(calls, 2);
  });

  it("keeps polling after a request fails", async (t) => {
    t.mock.timers.enable({ apis: ["setInterval"] });
    let calls = 0;
    const stop = startPoll(async () => {
      calls++;
      throw new Error("controller unavailable");
    }, 5_000);
    t.after(stop);
    await settle();
    t.mock.timers.tick(5_000);
    assert.equal(calls, 2);
  });

  it("skips ticks while the tab is hidden and stops when told", async (t) => {
    t.mock.timers.enable({ apis: ["setInterval"] });
    let calls = 0;
    const stop = startPoll(async () => {
      calls++;
    }, 5_000);
    await settle();
    runtime.document = { hidden: true };
    t.mock.timers.tick(5_000);
    assert.equal(calls, 1);
    runtime.document = { hidden: false };
    stop();
    t.mock.timers.tick(5_000);
    assert.equal(calls, 1);
  });
});
