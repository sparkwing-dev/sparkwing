import assert from "node:assert/strict";
import test from "node:test";
import type { Node, QueueState } from "./api";
import {
  appendHostPressureSample,
  hostPressureSample,
  startSerialPolling,
  summarizeNodeResources,
} from "./resourceObservability";

function queue(resources: QueueState["resources"]): QueueState {
  return { resources } as QueueState;
}

test("host pressure preserves measured external load and host reserve", () => {
  const sample = hostPressureSample(
    queue([
      {
        key: "cores",
        capacity: 16,
        held: 6,
        reserved: 2,
        external: 3,
        available: 5,
      },
      {
        key: "memory",
        capacity: 32 * 2 ** 30,
        held: 8 * 2 ** 30,
        reserved: 4 * 2 ** 30,
        external: 6 * 2 ** 30,
        available: 14 * 2 ** 30,
      },
    ]),
    123,
  );

  assert.deepEqual(sample, {
    at: 123,
    cpu: { capacity: 16, held: 6, reserved: 2, external: 3 },
    memory: {
      capacity: 32 * 2 ** 30,
      held: 8 * 2 ** 30,
      reserved: 4 * 2 ** 30,
      external: 6 * 2 ** 30,
    },
  });
});

test("host pressure keeps an unavailable external sensor distinct from zero", () => {
  const sample = hostPressureSample(
    queue([
      {
        key: "cores",
        capacity: 8,
        held: 2,
        reserved: 1,
        external: 0,
        external_source: "unmeasured",
        available: 5,
      },
    ]),
    456,
  );

  assert.equal(sample?.cpu?.external, null);
});

test("host pressure history is bounded", () => {
  const state = queue([
    { key: "cores", capacity: 8, held: 1, available: 7 },
  ]);
  let samples = appendHostPressureSample([], state, 1, 2);
  samples = appendHostPressureSample(samples, state, 2, 2);
  samples = appendHostPressureSample(samples, state, 3, 2);
  assert.deepEqual(
    samples.map((sample) => sample.at),
    [2, 3],
  );
});

test("host pressure history drops samples outside five minutes", () => {
  const state = queue([
    { key: "cores", capacity: 8, held: 1, available: 7 },
  ]);
  const old = appendHostPressureSample([], state, 1);
  const samples = appendHostPressureSample(old, state, 5 * 60 * 1000 + 2);
  assert.deepEqual(
    samples.map((sample) => sample.at),
    [5 * 60 * 1000 + 2],
  );
});

test("serial polling waits for one response and drops a cancelled response", async () => {
  let resolveFirst!: (value: string) => void;
  let resolveSecond!: (value: string) => void;
  const first = new Promise<string>((resolve) => {
    resolveFirst = resolve;
  });
  const second = new Promise<string>((resolve) => {
    resolveSecond = resolve;
  });
  let secondStarted!: () => void;
  const secondLoad = new Promise<void>((resolve) => {
    secondStarted = resolve;
  });
  let loads = 0;
  const published: string[] = [];
  const stop = startSerialPolling({
    load: () => {
      loads++;
      if (loads === 2) secondStarted();
      return loads === 1 ? first : second;
    },
    publish: (value) => published.push(value),
    intervalMS: 0,
  });

  await new Promise<void>((resolve) => setImmediate(resolve));
  assert.equal(loads, 1);
  resolveFirst("first");
  await secondLoad;
  assert.equal(loads, 2);
  assert.deepEqual(published, ["first"]);

  stop();
  resolveSecond("stale");
  await new Promise<void>((resolve) => setImmediate(resolve));
  assert.deepEqual(published, ["first"]);
});

test("node summary separates reservations, exact totals, and command samples", () => {
  const node: Node = {
    id: "build",
    status: "done",
    outcome: "success",
    deps: [],
    duration_ms: 2000,
    requested_cores: 2.5,
    requested_memory_bytes: 4 * 2 ** 30,
    cpu_nanos: 3_000_000_000,
    process_wall_nanos: 2_000_000_000,
    max_rss_bytes: 900 << 20,
  };
  const summary = summarizeNodeResources(node, {
    points: [
      {
        ts: "2026-09-16T00:00:00Z",
        cpu_millicores: 750,
        memory_bytes: 500 << 20,
      },
      {
        ts: "2026-09-16T00:00:02Z",
        cpu_millicores: 2200,
        memory_bytes: 800 << 20,
        cpu_time_nanos: 1_250_000_000,
      },
    ],
  });

  assert.deepEqual(summary, {
    samplerCount: 1,
    commandCount: 1,
    sampledPeakCPUMillicores: 750,
    sampledPeakMemoryBytes: 500 << 20,
    commandPeakCPUMillicores: 2200,
    commandPeakMemoryBytes: 800 << 20,
    requestedCPUMillicores: 2500,
    requestedMemoryBytes: 4 * 2 ** 30,
    exactCPUTimeNanos: 3_000_000_000,
    exactMeanCPUMillicores: 1500,
    exactMaxRSSBytes: 900 << 20,
    commandCPUTimeNanos: 1_250_000_000,
  });
});
