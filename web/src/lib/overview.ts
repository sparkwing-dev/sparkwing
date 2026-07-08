// Executive-summary metrics for the home dashboard. Pure functions
// over the runs list so the home page reads as current-state-plus-trend
// rather than a feed of historical failures. Each metric pairs a value
// over a recent window with the same-length window shifted back to an
// adjustable anchor, yielding a +/- delta that reads "improved" or
// "regressed" at a glance.
//
// A "deploy" here is a completed run: one that reached a terminal state
// (it has a finished_at). The success rate counts success against
// success+failed and ignores cancelled runs, which carry no pass/fail
// signal.

import type { Run } from "./api";

function durationMs(r: Run): number {
  if (!r.finished_at) return 0;
  return new Date(r.finished_at).getTime() - new Date(r.started_at).getTime();
}

export const DAY_MS = 24 * 60 * 60 * 1000;
export const WEEK_MS = 7 * DAY_MS;

// Anchor options the home page offers for the comparison baseline. The
// anchor is how far back the previous-period window sits; the default
// is one week.
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
  // Current value over the metric's window; null when undefined (e.g. a
  // success rate with no completed runs).
  current: number | null;
  // Same value over the equal-length window ending at the anchor.
  previous: number | null;
  // Whether a higher value is the better outcome. Drives delta coloring:
  // deploy counts and success rate improve when they rise; build time
  // improves when it falls.
  higherIsBetter: boolean;
}

export interface Overview {
  buildTime: Metric;
  deploys1d: Metric;
  deploys7d: Metric;
  successRate: Metric;
  lastDeploy: Run | null;
}

// completedAt returns the terminal time of a run in epoch ms, or NaN
// when the run has not finished or carries no usable timestamp.
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

// completedIn returns the runs that reached a terminal state within
// [start, end).
export function completedIn(runs: Run[], start: number, end: number): Run[] {
  return runs.filter((r) => inWindow(r, start, end));
}

export function deployCount(runs: Run[], start: number, end: number): number {
  return completedIn(runs, start, end).length;
}

// successRate returns success / (success + failed) over the window as a
// 0..1 fraction, or null when no run resolved to pass or fail. Cancelled
// runs are excluded from both numerator and denominator.
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

// medianBuildMs returns the median wall-clock duration of successful
// runs in the window, or null when none succeeded. Median resists the
// long-tail outliers that drag a mean.
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

// lastDeploy returns the most recently completed run, or null when none
// have finished.
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

// deltaAbs is the signed change current - previous, or null when either
// side is undefined.
export function deltaAbs(m: Metric): number | null {
  if (m.current == null || m.previous == null) return null;
  return m.current - m.previous;
}

// deltaPct is the fractional change vs the previous value, or null when
// the previous value is undefined or zero.
export function deltaPct(m: Metric): number | null {
  if (m.current == null || m.previous == null || m.previous === 0) return null;
  return (m.current - m.previous) / m.previous;
}

// summarize builds the executive-summary metrics from a runs list. now
// is injected for testability; anchorMs is how far back the comparison
// window sits.
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
