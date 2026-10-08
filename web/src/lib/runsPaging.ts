import type { PipelineMeta, Run } from "@/lib/api";
import { parseLooseDate, type RunFilterState } from "@/components/RunFilters";

export const RUNS_PAGE_SIZE = 50;

// A cursor names one run by the controller's sort key. The instant stays a decimal
// string of Unix nanoseconds, because a JavaScript number past 2^53 rounds it to a
// different instant and the next page would skip or repeat runs.
export interface RunCursor {
  startedAt: string;
  id: string;
}

export type RunsPageRef =
  | { kind: "latest" }
  | { kind: "older"; cursor: RunCursor }
  | { kind: "newer"; cursor: RunCursor };

export const OLDER_PARAM = "older";
export const NEWER_PARAM = "newer";

export function cursorForRun(run: Pick<Run, "id" | "started_at">): RunCursor {
  const m = /^(.*T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/.exec(
    run.started_at ?? "",
  );
  const ms = m ? Date.parse(m[1] + m[3]) : NaN;
  // The controller files every run that never started under instant zero.
  if (!m || !Number.isFinite(ms) || ms <= 0)
    return { startedAt: "0", id: run.id };
  const fraction = (m[2] ?? "").padEnd(9, "0");
  return { startedAt: `${ms / 1000}${fraction}`, id: run.id };
}

export function encodeCursor(c: RunCursor): string {
  return `${c.startedAt}:${c.id}`;
}

export function parseCursor(raw: string | null): RunCursor | null {
  if (!raw) return null;
  const sep = raw.indexOf(":");
  if (sep <= 0 || sep === raw.length - 1) return null;
  const startedAt = raw.slice(0, sep);
  if (!/^\d+$/.test(startedAt)) return null;
  return { startedAt, id: raw.slice(sep + 1) };
}

export function pageRefFromParams(params: URLSearchParams): RunsPageRef {
  const older = parseCursor(params.get(OLDER_PARAM));
  if (older) return { kind: "older", cursor: older };
  const newer = parseCursor(params.get(NEWER_PARAM));
  if (newer) return { kind: "newer", cursor: newer };
  return { kind: "latest" };
}

export function pageRefParams(ref: RunsPageRef): Record<string, string | null> {
  return {
    [OLDER_PARAM]: ref.kind === "older" ? encodeCursor(ref.cursor) : null,
    [NEWER_PARAM]: ref.kind === "newer" ? encodeCursor(ref.cursor) : null,
  };
}

type FilterValues = Omit<RunFilterState, `set${string}`>;

// A full RFC 3339 time passes through untouched, because Date keeps milliseconds and an
// inclusive bound copied from a run's nanosecond start would exclude that run.
const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$/;

function isoBound(raw: string): string | null {
  if (RFC3339.test(raw.trim())) return raw.trim();
  const ms = parseLooseDate(raw);
  return ms === null ? null : new Date(ms).toISOString();
}

// runListQuery turns the dashboard's filters into the controller's run-list query, so
// they select across every run rather than the page on screen. Tags belong to pipelines,
// so a tag filter becomes the pipelines carrying it. It answers null when the filters
// can match no run, which no query expresses.
export function runListQuery(
  s: FilterValues,
  pipelineMeta: Record<string, PipelineMeta>,
  ref: RunsPageRef,
  limit = RUNS_PAGE_SIZE,
): URLSearchParams | null {
  const params = new URLSearchParams();
  const list = (key: string, values: string[]) => {
    if (values.length) params.set(key, values.join(","));
  };
  const tagged = (tags: string[]) =>
    Object.entries(pipelineMeta)
      .filter(([, m]) => (m.tags ?? []).some((t) => tags.includes(t)))
      .map(([name]) => name);

  let pipelines = s.filterPipeline;
  if (s.filterTag.length) {
    const withTag = tagged(s.filterTag);
    pipelines = pipelines.length
      ? pipelines.filter((p) => withTag.includes(p))
      : withTag;
    if (pipelines.length === 0) return null;
  }
  const excludePipelines = [
    ...new Set([...s.excludePipeline, ...tagged(s.excludeTag)]),
  ];
  list("pipeline", pipelines);
  list("exclude_pipeline", excludePipelines);
  list("status", s.filterStatus);
  list("exclude_status", s.excludeStatus);
  list("trigger_source", s.filterTrigger);
  list("exclude_trigger_source", s.excludeTrigger);
  list("repo_name", s.filterRepo);
  list("exclude_repo_name", s.excludeRepo);
  list("git_branch", s.filterBranch);
  list("exclude_git_branch", s.excludeBranch);
  list("git_sha", s.filterCommit);
  list("exclude_git_sha", s.excludeCommit);
  for (const [key, raw] of [
    ["started_after", s.startedAfter],
    ["started_before", s.startedBefore],
    ["finished_after", s.finishedAfter],
    ["finished_before", s.finishedBefore],
  ] as const) {
    const bound = isoBound(raw);
    if (bound) params.set(key, bound);
  }
  if (s.filterText.trim()) params.set("q", s.filterText.trim());
  // One run past the page tells a last page from a full one.
  params.set("limit", String(limit + 1));
  if (ref.kind === "older") {
    params.set("after_started_at", ref.cursor.startedAt);
    params.set("after_id", ref.cursor.id);
  } else if (ref.kind === "newer") {
    params.set("before_started_at", ref.cursor.startedAt);
    params.set("before_id", ref.cursor.id);
  }
  return params;
}

export interface RunsPage {
  runs: Run[];
  // older and newer name the page on each side, or null at either end.
  older: RunsPageRef | null;
  newer: RunsPageRef | null;
  // atLatest is set when a walk toward newer runs reached the newest page, which the
  // caller shows as the latest page so it polls again.
  atLatest: boolean;
}

// pageFromResponse reads one page out of a response to runListQuery's limit+1 ask.
export function pageFromResponse(
  fetched: Run[],
  ref: RunsPageRef,
  limit = RUNS_PAGE_SIZE,
): RunsPage {
  if (ref.kind === "newer") {
    // A before-cursor answer is the runs just newer than the cursor, newest first, so the
    // probe row is the newest one.
    const more = fetched.length > limit;
    const runs = more ? fetched.slice(1) : fetched;
    return {
      runs,
      older: runs.length
        ? { kind: "older", cursor: cursorForRun(runs[runs.length - 1]) }
        : { kind: "older", cursor: ref.cursor },
      newer: more ? { kind: "newer", cursor: cursorForRun(runs[0]) } : null,
      atLatest: !more,
    };
  }
  const more = fetched.length > limit;
  const runs = more ? fetched.slice(0, limit) : fetched;
  return {
    runs,
    older: more
      ? { kind: "older", cursor: cursorForRun(runs[runs.length - 1]) }
      : null,
    newer:
      ref.kind === "latest"
        ? null
        : runs.length
          ? { kind: "newer", cursor: cursorForRun(runs[0]) }
          : { kind: "newer", cursor: ref.cursor },
    atLatest: ref.kind === "latest",
  };
}
