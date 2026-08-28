import assert from "node:assert/strict";
import { describe, it } from "node:test";
import {
  deferOnce,
  type DeferredMarker,
  type DeferredScheduler,
} from "./deferredOnce";

function fakeScheduler() {
  const pending: Array<{ active: boolean; callback: () => void }> = [];
  const schedule: DeferredScheduler = (callback) => {
    const task = { active: true, callback };
    pending.push(task);
    return () => {
      task.active = false;
    };
  };
  return {
    schedule,
    flush() {
      for (const task of pending.splice(0)) {
        if (task.active) task.callback();
      }
    },
  };
}

describe("deferOnce", () => {
  it("runs once across the StrictMode setup-cleanup-setup cycle", () => {
    const marker: DeferredMarker<string> = { current: null };
    const scheduler = fakeScheduler();
    let calls = 0;
    const action = () => {
      calls++;
      return true;
    };

    const cleanupFirst = deferOnce(marker, "run-1", scheduler.schedule, action);
    cleanupFirst();
    deferOnce(marker, "run-1", scheduler.schedule, action);
    scheduler.flush();

    assert.equal(calls, 1);
    assert.equal(marker.current, "run-1");
    deferOnce(marker, "run-1", scheduler.schedule, action);
    scheduler.flush();
    assert.equal(calls, 1);
  });

  it("cancels stale animation work when the key changes", () => {
    const marker: DeferredMarker<string> = { current: null };
    const scheduler = fakeScheduler();
    const calls: string[] = [];

    const cleanupFirst = deferOnce(marker, "step-a", scheduler.schedule, () => {
      calls.push("step-a");
      return true;
    });
    cleanupFirst();
    deferOnce(marker, "step-b", scheduler.schedule, () => {
      calls.push("step-b");
      return true;
    });
    scheduler.flush();

    assert.deepEqual(calls, ["step-b"]);
    assert.equal(marker.current, "step-b");
  });

  it("leaves failed deferred work retryable", () => {
    const marker: DeferredMarker<string> = { current: null };
    const scheduler = fakeScheduler();

    deferOnce(marker, "line-7", scheduler.schedule, () => false);
    scheduler.flush();
    assert.equal(marker.current, null);

    deferOnce(marker, "line-7", scheduler.schedule, () => true);
    scheduler.flush();
    assert.equal(marker.current, "line-7");
  });
});
