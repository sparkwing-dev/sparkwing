
import type { Run } from "./api";

function durationMs(r: Run): number {
  if (!r.finished_at) return 0;
  return new Date(r.finished_at).getTime() - new Date(r.started_at).getTime();
}

export const DAY_MS = 24 * 60 * 60 * 1000;
export const WEEK_MS = 7 * DAY_MS;

export const ANCHOR_OPTIONS: ReadonlyArray<{ label: string; ms: number }> = [
  { label: "1d ago", ms: DAY_MS },
  { label: "1w ago", ms: WEEK_MS },
  { label: "30d ago", ms: 30 * DAY_MS },
];

export const DEFAULT_ANCHOR_MS = WEEK_MS;

export type MetricUnit = "count" | "pct" | "ms";

export interface Metric {
  key: string;
  label: string;
  unit: MetricUnit;
  current: number | null;
  previous: number | null;
  higherIsBetter: boolean;
}

export interface Overview {
  buildTime: Metric;
  deploys1d: Metric;
  deploys7d: Metric;
  successRate: Metric;
  lastDeploy: Run | null;
}

export function completedAt(r: Run): number {
  if (!r.finished_at) return NaN;
  const t = new Date(r.finished_at).getTime();
  return Number.isNaN(t) ? NaN : t;
}

export function isCompleted(r: Run): boolean {
  return Number.isFinite(completedAt(r));
}

function inWindow(r: Run, start: number, end: number): boolean {
  const t = completedAt(r);
  return Number.isFinite(t) && t >= start && t < end;
}

export function completedIn(runs: Run[], start: number, end: number): Run[] {
  return runs.filter((r) => inWindow(r, start, end));
}

export function deployCount(runs: Run[], start: number, end: number): number {
  return completedIn(runs, start, end).length;
}

export function successRate(
  runs: Run[],
  start: number,
  end: number,
): number | null {
  let success = 0;
  let resolved = 0;
  for (const r of completedIn(runs, start, end)) {
    if (r.status === "success") {
      success += 1;
      resolved += 1;
    } else if (r.status === "failed") {
      resolved += 1;
    }
  }
  if (resolved === 0) return null;
  return success / resolved;
}

export function medianBuildMs(
  runs: Run[],
  start: number,
  end: number,
): number | null {
  const durations = completedIn(runs, start, end)
    .filter((r) => r.status === "success")
    .map(durationMs)
    .filter((ms) => ms > 0)
    .sort((a, b) => a - b);
  if (durations.length === 0) return null;
  const mid = Math.floor(durations.length / 2);
  if (durations.length % 2 === 1) return durations[mid];
  return (durations[mid - 1] + durations[mid]) / 2;
}

export function lastDeploy(runs: Run[]): Run | null {
  let best: Run | null = null;
  let bestAt = -Infinity;
  for (const r of runs) {
    const t = completedAt(r);
    if (Number.isFinite(t) && t > bestAt) {
      best = r;
      bestAt = t;
    }
  }
  return best;
}

export function deltaAbs(m: Metric): number | null {
  if (m.current == null || m.previous == null) return null;
  return m.current - m.previous;
}

export function deltaPct(m: Metric): number | null {
  if (m.current == null || m.previous == null || m.previous === 0) return null;
  return (m.current - m.previous) / m.previous;
}

export function summarize(
  runs: Run[],
  now: number,
  anchorMs: number = DEFAULT_ANCHOR_MS,
): Overview {
  const win = (windowMs: number) => ({
    curStart: now - windowMs,
    curEnd: now,
    prevStart: now - anchorMs - windowMs,
    prevEnd: now - anchorMs,
  });

  const buildW = win(WEEK_MS);
  const day = win(DAY_MS);
  const week = win(WEEK_MS);

  return {
    buildTime: {
      key: "build_time",
      label: "Build time (7d median)",
      unit: "ms",
      current: medianBuildMs(runs, buildW.curStart, buildW.curEnd),
      previous: medianBuildMs(runs, buildW.prevStart, buildW.prevEnd),
      higherIsBetter: false,
    },
    deploys1d: {
      key: "deploys_1d",
      label: "Deploys (24h)",
      unit: "count",
      current: deployCount(runs, day.curStart, day.curEnd),
      previous: deployCount(runs, day.prevStart, day.prevEnd),
      higherIsBetter: true,
    },
    deploys7d: {
      key: "deploys_7d",
      label: "Deploys (7d)",
      unit: "count",
      current: deployCount(runs, week.curStart, week.curEnd),
      previous: deployCount(runs, week.prevStart, week.prevEnd),
      higherIsBetter: true,
    },
    successRate: {
      key: "success_rate",
      label: "Success rate (7d)",
      unit: "pct",
      current: successRate(runs, week.curStart, week.curEnd),
      previous: successRate(runs, week.prevStart, week.prevEnd),
      higherIsBetter: true,
    },
    lastDeploy: lastDeploy(runs),
  };
}
