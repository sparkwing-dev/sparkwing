import type { Run } from "./api";

export function hasTimestamp(ts?: string): ts is string {
  return Boolean(ts && !ts.startsWith("0001-") && Number.isFinite(Date.parse(ts)));
}

export function runStartedAt(run: Pick<Run, "started_at">): string | undefined {
  return hasTimestamp(run.started_at) ? run.started_at : undefined;
}

export function runTimestamp(run: Pick<Run, "started_at" | "created_at">): string | undefined {
  return runStartedAt(run) ?? (hasTimestamp(run.created_at) ? run.created_at : undefined);
}

export function runElapsedMs(run: Pick<Run, "started_at" | "finished_at">, now = Date.now()): number | undefined {
  const start = runStartedAt(run);
  if (!start) return undefined;
  const end = hasTimestamp(run.finished_at) ? Date.parse(run.finished_at) : now;
  return Math.max(0, end - Date.parse(start));
}
