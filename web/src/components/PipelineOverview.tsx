"use client";

// PipelineOverview: the "by pipeline" pivot of run history.
//
// Lists every pipeline discovered from /api/v1/pipelines and annotates
// it with recent-run stats from /api/runs. Same-name pipelines in
// different repos render as separate rows (key = repo/pipeline). Click
// a row to expand recent runs + trigger form. Used as a tab on /runs
// and as the body of /pipeline-overview (which is a redirect alias).

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import {
  type PipelineMeta,
  type Run,
  getPipelines,
  getRuns,
  runDurationMs,
} from "@/lib/api";
import TriggerForm from "@/components/TriggerForm";
import Tooltip from "@/components/Tooltip";
import {
  type FilterCtx,
  FilterableValue,
  FullFilterBar,
  buildGroupsFromState,
  clearAllFilters,
  computeOptions,
  runMatchesFilter,
  useFilterCtx,
  useFilterDropdownState,
  useUrlFilterState,
} from "@/components/RunFilters";
import {
  fmtAgo,
  fmtClock,
  fmtDatePrefix,
  fmtFullDate,
  fmtMs,
} from "@/lib/timeFormat";

const POLL_MS = 5000;
const RUNS_WINDOW = 200;
const SPARK_SIZE = 30;

interface PipelineRow {
  // Composite identity: `repo/pipeline` when run history attaches a
  // repo, bare pipeline name for registry-only entries. Used as the
  // expand/trigger state key so two pipelines that share a name across
  // different repos don't collide.
  key: string;
  pipeline: string;
  repo: string | null;
  meta: PipelineMeta | null;
  runs: Run[];
  lastRun: Run | null;
  stats: { total: number; passed: number; failed: number; running: number };
  avgDurMs: number;
}

function repoLabel(r: Run): string | null {
  const raw = r.repo || r.github_repo;
  if (!raw) return null;
  const slash = raw.lastIndexOf("/");
  return slash >= 0 ? raw.slice(slash + 1) : raw;
}

function buildRows(
  registry: Record<string, PipelineMeta>,
  runs: Run[],
): PipelineRow[] {
  const runsByKey = new Map<
    string,
    { pipeline: string; repo: string | null; runs: Run[] }
  >();
  for (const r of runs) {
    const repo = repoLabel(r);
    const key = repo ? `${repo}/${r.pipeline}` : r.pipeline;
    const bucket = runsByKey.get(key);
    if (bucket) bucket.runs.push(r);
    else runsByKey.set(key, { pipeline: r.pipeline, repo, runs: [r] });
  }

  const out: PipelineRow[] = [];
  for (const [key, { pipeline, repo, runs: pipelineRuns }] of runsByKey) {
    const passed = pipelineRuns.filter((r) => r.status === "success").length;
    const failed = pipelineRuns.filter((r) => r.status === "failed").length;
    const running = pipelineRuns.filter((r) => r.status === "running").length;
    const finished = pipelineRuns.filter(
      (r) => r.finished_at && runDurationMs(r) > 0,
    );
    const avgDurMs = finished.length
      ? finished.reduce((a, r) => a + runDurationMs(r), 0) / finished.length
      : 0;
    out.push({
      key,
      pipeline,
      repo,
      meta: registry[pipeline] || null,
      runs: pipelineRuns,
      lastRun: pipelineRuns[0] || null,
      stats: { total: pipelineRuns.length, passed, failed, running },
      avgDurMs,
    });
  }

  // Registry-only rows: pipelines registered in pipelines.yaml that
  // haven't been run yet in any repo we've seen. The registry doesn't
  // carry repo info, so these render with no repo prefix.
  const seenPipelines = new Set(
    Array.from(runsByKey.values()).map((v) => v.pipeline),
  );
  for (const name of Object.keys(registry)) {
    if (seenPipelines.has(name)) continue;
    out.push({
      key: name,
      pipeline: name,
      repo: null,
      meta: registry[name],
      runs: [],
      lastRun: null,
      stats: { total: 0, passed: 0, failed: 0, running: 0 },
      avgDurMs: 0,
    });
  }

  return out;
}

function sortRows(a: PipelineRow, b: PipelineRow): number {
  const at = a.lastRun ? new Date(a.lastRun.started_at).getTime() : 0;
  const bt = b.lastRun ? new Date(b.lastRun.started_at).getTime() : 0;
  if (at !== bt) return bt - at;
  return a.key.localeCompare(b.key);
}

export default function PipelineOverview({
  pivotTabs,
}: {
  pivotTabs?: React.ReactNode;
} = {}) {
  const [registry, setRegistry] = useState<Record<string, PipelineMeta>>({});
  const [runs, setRuns] = useState<Run[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [triggerOpen, setTriggerOpen] = useState<string | null>(null);
  const filterState = useUrlFilterState();
  const filterCtx = useFilterCtx(filterState);
  const { openDropdown, setOpenDropdown, filterRef } = useFilterDropdownState();
  const searchParams = useSearchParams();
  const router = useRouter();
  const selectedRun = searchParams.get("run");
  // Click on a run in the by-pipeline view jumps to the Activity
  // pivot with that run selected so the user can dive into the detail
  // panel + scroll context. The runs page picks the row id up from
  // the URL and scrolls it into view on mount.
  const openRunInActivity = useCallback(
    (id: string) => {
      const params = new URLSearchParams(searchParams.toString());
      params.delete("view");
      params.set("run", id);
      const qs = params.toString();
      router.push(qs ? `/runs?${qs}` : "/runs", { scroll: false });
    },
    [router, searchParams],
  );

  // toggleRunHighlight just flips the ?run= param without changing
  // pivot, so the user can click blank space on a row to mark it as
  // the focused run and click again to clear.
  const toggleRunHighlight = useCallback(
    (id: string) => {
      const params = new URLSearchParams(searchParams.toString());
      if (params.get("run") === id) params.delete("run");
      else params.set("run", id);
      const qs = params.toString();
      router.replace(qs ? `/runs?${qs}` : "/runs", { scroll: false });
    },
    [router, searchParams],
  );

  const refresh = useCallback(async () => {
    const [reg, rs] = await Promise.all([
      getPipelines(),
      getRuns({ limit: RUNS_WINDOW }),
    ]);
    setRegistry(reg);
    setRuns(rs);
    setLoaded(true);
  }, []);

  useEffect(() => {
    refresh();
    const i = window.setInterval(() => {
      if (!document.hidden) refresh();
    }, POLL_MS);
    return () => window.clearInterval(i);
  }, [refresh]);

  // Filter the underlying runs first, then build rows from what's
  // left so per-pipeline stats reflect only matching runs.
  const filteredRuns = useMemo(
    () => runs.filter((r) => runMatchesFilter(r, filterState, registry)),
    [runs, filterState, registry],
  );

  const rows = useMemo(
    () => buildRows(registry, filteredRuns).sort(sortRows),
    [registry, filteredRuns],
  );

  // Auto-expand the row containing the selected run on entry / when
  // the selection changes. Only fires once per selectedRun value so
  // poll-driven row rebuilds don't re-open a card the user closed.
  const autoExpandedForRef = useRef<string | null>(null);
  useEffect(() => {
    if (!selectedRun) return;
    if (autoExpandedForRef.current === selectedRun) return;
    const row = rows.find((r) => r.runs.some((rr) => rr.id === selectedRun));
    if (!row) return;
    autoExpandedForRef.current = selectedRun;
    setExpanded((cur) => {
      if (cur.has(row.key)) return cur;
      const next = new Set(cur);
      next.add(row.key);
      return next;
    });
  }, [selectedRun, rows]);

  // Scroll the selected run into view once the row is expanded and
  // rendered. Tracked per-id so polls don't keep re-scrolling.
  const scrolledForRef = useRef<string | null>(null);
  useEffect(() => {
    if (!selectedRun) return;
    if (scrolledForRef.current === selectedRun) return;
    const el = document.querySelector(
      `[data-run-id="${selectedRun}"]`,
    ) as HTMLElement | null;
    if (!el) return;
    scrolledForRef.current = selectedRun;
    el.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [selectedRun, expanded, rows]);

  const options = useMemo(
    () => computeOptions(runs, registry),
    [runs, registry],
  );
  const groups = buildGroupsFromState(filterState, options);
  const dateGroup = {
    startedAfter: filterState.startedAfter,
    startedBefore: filterState.startedBefore,
    finishedAfter: filterState.finishedAfter,
    finishedBefore: filterState.finishedBefore,
    setStartedAfter: filterState.setStartedAfter,
    setStartedBefore: filterState.setStartedBefore,
    setFinishedAfter: filterState.setFinishedAfter,
    setFinishedBefore: filterState.setFinishedBefore,
  };

  const filtered = rows;

  const totals = useMemo(() => {
    let passed = 0;
    let failed = 0;
    let running = 0;
    for (const r of rows) {
      passed += r.stats.passed;
      failed += r.stats.failed;
      running += r.stats.running;
    }
    return {
      pipelines: rows.length,
      registered: Object.keys(registry).length,
      runs: runs.length,
      passed,
      failed,
      running,
    };
  }, [rows, registry, runs]);

  return (
    <div className="flex flex-col flex-1 overflow-hidden">
      <div
        ref={filterRef}
        className="border-b border-[var(--border)] flex items-center bg-[var(--surface)] shrink-0"
      >
        {pivotTabs}
        <FullFilterBar
          openDropdown={openDropdown}
          setOpenDropdown={setOpenDropdown}
          groups={groups}
          dateGroup={dateGroup}
          searchText={filterState.filterText}
          setSearchText={filterState.setFilterText}
          onClearAll={() => clearAllFilters(filterState)}
        />
      </div>

      <div className="flex-1 overflow-y-auto p-6 max-w-6xl mx-auto w-full">
        <div className="flex items-baseline justify-between mb-4">
          <h1 className="text-xl font-bold">Pipelines</h1>
          <span className="text-[10px] font-mono text-[var(--muted)]">
            /api/v1/pipelines + /api/runs - refresh every {POLL_MS / 1000}s
          </span>
        </div>

        <SummaryCards totals={totals} />

        {!loaded ? (
          <Panel>Loading pipelines...</Panel>
        ) : filtered.length === 0 ? (
          <EmptyPanel
            hasFilter={filterState.filterText.trim().length > 0}
            hasRegistry={totals.registered > 0}
          />
        ) : (
          <div className="space-y-2">
            {filtered.map((row) => (
              <PipelineCard
                key={row.key}
                row={row}
                expanded={expanded.has(row.key)}
                onToggle={() =>
                  setExpanded((cur) => {
                    const next = new Set(cur);
                    if (next.has(row.key)) next.delete(row.key);
                    else next.add(row.key);
                    return next;
                  })
                }
                triggerOpen={triggerOpen === row.key}
                onTrigger={() =>
                  setTriggerOpen((cur) => (cur === row.key ? null : row.key))
                }
                onTriggered={() => {
                  setTriggerOpen(null);
                  refresh();
                }}
                selectedRun={selectedRun}
                onSelectRun={openRunInActivity}
                onHighlightRun={toggleRunHighlight}
                ctx={filterCtx}
              />
            ))}
          </div>
        )}

        <Footer />
      </div>
    </div>
  );
}

function SummaryCards({
  totals,
}: {
  totals: {
    pipelines: number;
    registered: number;
    runs: number;
    passed: number;
    failed: number;
    running: number;
  };
}) {
  const cards = [
    {
      label: "pipelines",
      value: totals.pipelines,
      tip: "Pipelines that match the current filters",
    },
    {
      label: "registered",
      value: totals.registered,
      tip: "Pipelines declared in pipelines.yaml",
    },
    {
      label: `runs (last ${RUNS_WINDOW})`,
      value: totals.runs,
      tip: `Most recent ${RUNS_WINDOW} runs are loaded; filters apply within that window`,
    },
    {
      label: "passed",
      value: totals.passed,
      tip: "Runs that finished successfully",
    },
    {
      label: "failed",
      value: totals.failed,
      tip: "Runs that finished with a failure or cancellation",
    },
    {
      label: "running",
      value: totals.running,
      tip: "Runs currently in flight",
    },
  ];
  return (
    <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-3 mb-4">
      {cards.map((c) => (
        <Tooltip key={c.label} content={c.tip}>
          <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg px-3 py-2 cursor-help">
            <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
              {c.label}
            </div>
            <div className="text-lg font-mono mt-0.5">{c.value}</div>
          </div>
        </Tooltip>
      ))}
    </div>
  );
}

function PipelineCard({
  row,
  expanded,
  onToggle,
  triggerOpen,
  onTrigger,
  onTriggered,
  selectedRun,
  onSelectRun,
  onHighlightRun,
  ctx,
}: {
  row: PipelineRow;
  expanded: boolean;
  onToggle: () => void;
  triggerOpen: boolean;
  onTrigger: () => void;
  onTriggered: () => void;
  selectedRun: string | null;
  onSelectRun: (id: string) => void;
  onHighlightRun: (id: string) => void;
  ctx: FilterCtx;
}) {
  const { stats } = row;
  const successRate =
    stats.total === 0
      ? null
      : Math.round((stats.passed / Math.max(1, stats.total)) * 100);
  const recent = row.runs.slice(0, SPARK_SIZE);
  const tags = row.meta?.tags || [];

  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg overflow-hidden">
      <button
        onClick={onToggle}
        className="w-full flex items-center gap-3 px-3 py-2.5 text-left hover:bg-[var(--surface-raised,var(--background))] transition-colors"
      >
        <span className="w-4 text-center text-xs text-[var(--muted)]">
          {expanded ? "-" : "+"}
        </span>
        <span className="font-mono text-sm font-medium truncate flex-1 min-w-0">
          {row.repo && (
            <>
              <FilterableValue
                facet="repo"
                value={row.repo}
                ctx={ctx}
                tooltip={`Repo: ${row.repo}`}
              >
                <span className="text-cyan-400/80">{row.repo}</span>
              </FilterableValue>
              <span className="text-[var(--muted)] mx-1">/</span>
            </>
          )}
          <FilterableValue
            facet="pipeline"
            value={row.pipeline}
            ctx={ctx}
            tooltip={`Pipeline: ${row.pipeline}`}
          >
            <span className="text-violet-300">{row.pipeline}</span>
          </FilterableValue>
        </span>
        {!row.meta && (
          <Tooltip content="Run history exists but pipeline is not in the local pipelines.yaml registry">
            <span className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-amber-400/15 text-amber-300">
              runs-only
            </span>
          </Tooltip>
        )}
        {tags.map((t) => (
          <FilterableValue
            key={t}
            facet="tag"
            value={t}
            ctx={ctx}
            tooltip={`Tag: ${t}`}
          >
            <span className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-[var(--background)] text-[var(--muted)]">
              {t}
            </span>
          </FilterableValue>
        ))}
        <Sparkline runs={recent} />
        <Tooltip
          content={
            successRate === null
              ? "No completed runs in window"
              : `${stats.passed} passed out of ${stats.total} runs`
          }
        >
          <span className="text-xs text-[var(--muted)] font-mono w-20 text-right inline-block">
            {successRate === null ? "-" : `${successRate}%`}
          </span>
        </Tooltip>
        <Tooltip
          content={`${stats.total} total · ${stats.passed} passed · ${stats.failed} failed · ${stats.running} running`}
        >
          <span className="text-xs text-[var(--muted)] font-mono w-24 text-right inline-block">
            {stats.total} run{stats.total === 1 ? "" : "s"}
          </span>
        </Tooltip>
        <Tooltip
          content={
            row.lastRun
              ? `Last run ${fmtFullDate(row.lastRun.started_at)}`
              : "No runs yet in window"
          }
        >
          <span className="text-xs text-[var(--muted)] font-mono w-24 text-right inline-block">
            {row.lastRun ? (
              <TimeAgo ts={row.lastRun.started_at} />
            ) : (
              "never run"
            )}
          </span>
        </Tooltip>
      </button>

      {expanded && (
        <div className="border-t border-[var(--border)] px-3 py-3 space-y-3 text-xs">
          <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
            <KV
              label="passed"
              value={stats.passed}
              tip="Runs that finished successfully"
            />
            <KV
              label="failed"
              value={stats.failed}
              tip="Runs that finished with failure or cancellation"
            />
            <KV
              label="running"
              value={stats.running}
              tip="Runs currently in flight"
            />
            <KV
              label="avg duration"
              value={row.avgDurMs ? formatDuration(row.avgDurMs) : "-"}
              tip="Average duration across completed runs in window"
            />
          </div>

          <div className="flex items-center gap-2">
            <button
              onClick={onTrigger}
              className="text-xs bg-green-500/20 text-green-400 border border-green-500/30 rounded px-2 py-1 font-medium hover:bg-green-500/30"
            >
              {triggerOpen ? "close" : "+ Run this pipeline"}
            </button>
          </div>

          {triggerOpen && (
            <TriggerForm
              pipeline={row.pipeline}
              onTriggered={onTriggered}
              onClose={() => onTrigger()}
            />
          )}

          <RecentRuns
            runs={row.runs.slice(0, 15)}
            selectedRun={selectedRun}
            onSelectRun={onSelectRun}
            onHighlightRun={onHighlightRun}
            ctx={ctx}
          />
        </div>
      )}
    </div>
  );
}

function Sparkline({ runs }: { runs: Run[] }) {
  // Oldest-left, newest-right so new-runs-arrive animates from the right.
  const ordered = [...runs].reverse();
  const filler = Math.max(0, SPARK_SIZE - ordered.length);
  return (
    <div className="flex items-center gap-0.5">
      {Array.from({ length: filler }).map((_, i) => (
        <span
          key={`f${i}`}
          className="block w-1.5 h-3 rounded-sm bg-[var(--border)]"
        />
      ))}
      {ordered.map((r) => (
        <Tooltip key={r.id} content={<RunSummaryTip run={r} />}>
          <span
            className={`inline-block align-middle w-1.5 h-3 rounded-sm ${sparkColor(r.status)}`}
          />
        </Tooltip>
      ))}
    </div>
  );
}

function RunSummaryTip({ run }: { run: Run }) {
  const dur = runDurationMs(run);
  return (
    <div className="flex flex-col gap-0.5 font-mono">
      <div className="text-[var(--foreground)]">{run.id}</div>
      <div>
        <span className="text-[var(--muted)]">status </span>
        <span>{run.status}</span>
      </div>
      <div>
        <span className="text-[var(--muted)]">started </span>
        <span>{fmtFullDate(run.started_at)}</span>
      </div>
      {run.finished_at && (
        <div>
          <span className="text-[var(--muted)]">finished </span>
          <span>{fmtFullDate(run.finished_at)}</span>
        </div>
      )}
      {dur > 0 && (
        <div>
          <span className="text-[var(--muted)]">duration </span>
          <span>{fmtMs(dur)}</span>
        </div>
      )}
      <div>
        <span className="text-[var(--muted)]">{fmtAgo(run.started_at)}</span>
      </div>
    </div>
  );
}

function sparkColor(status: string): string {
  switch (status) {
    case "success":
      return "bg-green-400";
    case "failed":
      return "bg-red-400";
    case "running":
      return "bg-indigo-400 animate-pulse";
    case "cancelled":
      return "bg-amber-400";
    default:
      return "bg-slate-500";
  }
}

function RecentRuns({
  runs,
  selectedRun,
  onSelectRun,
  onHighlightRun,
  ctx,
}: {
  runs: Run[];
  selectedRun: string | null;
  onSelectRun: (id: string) => void;
  onHighlightRun: (id: string) => void;
  ctx: FilterCtx;
}) {
  if (runs.length === 0) {
    return (
      <div className="text-[var(--muted)]">
        No runs yet for this pipeline in the current window.
      </div>
    );
  }
  return (
    <div>
      <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1">
        recent runs
      </div>
      <ul className="divide-y divide-[var(--border)] border border-[var(--border)] rounded">
        {runs.map((r) => {
          const isSelected = selectedRun === r.id;
          return (
            <li
              key={r.id}
              data-run-id={r.id}
              onClick={() => onHighlightRun(r.id)}
              className={`px-2 py-1.5 grid items-center gap-x-1 gap-y-0 grid-cols-[0.5rem_11.5rem_3.5rem_9rem_minmax(0,1fr)_4.5rem_auto] cursor-pointer hover:bg-[var(--surface-raised)] transition-colors ${
                isSelected
                  ? "bg-violet-500/15 border-l-4 border-l-violet-400"
                  : "border-l-4 border-l-transparent"
              }`}
            >
              <FilterableValue
                facet="status"
                value={r.status}
                ctx={ctx}
                tooltip={<RunSummaryTip run={r} />}
              >
                <span
                  className={`inline-block align-middle w-1.5 h-1.5 rounded-full ${sparkColor(r.status)}`}
                />
              </FilterableValue>
              <Tooltip content={`Run: ${r.id}`}>
                <button
                  onClick={(e) => {
                    e.stopPropagation();
                    onSelectRun(r.id);
                  }}
                  className={`font-mono text-xs truncate min-w-0 text-left cursor-pointer hover:underline ${
                    isSelected ? "text-violet-200" : "text-[var(--accent)]"
                  }`}
                >
                  {r.id}
                </button>
              </Tooltip>
              {r.git_sha ? (
                <FilterableValue
                  facet="commit"
                  value={r.git_sha.slice(0, 7)}
                  ctx={ctx}
                  tooltip={`Commit: ${r.git_sha}`}
                >
                  <span className="text-[11px] text-[var(--muted)] font-mono truncate">
                    {r.git_sha.slice(0, 7)}
                  </span>
                </FilterableValue>
              ) : (
                <span />
              )}
              {r.git_branch ? (
                <FilterableValue
                  facet="branch"
                  value={r.git_branch}
                  ctx={ctx}
                  tooltip={`Branch: ${r.git_branch}`}
                >
                  <span className="text-[11px] text-amber-400/70 font-mono truncate">
                    ⎇{" "}
                    {r.git_branch.length > 20
                      ? r.git_branch.slice(0, 19) + "…"
                      : r.git_branch}
                  </span>
                </FilterableValue>
              ) : (
                <span />
              )}
              <RunSummary run={r} />
              <div className="justify-self-end">
                <RunDurationCell run={r} />
              </div>
              <div className="justify-self-end">
                <RunTimestampBlock run={r} />
              </div>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

function RunSummary({ run }: { run: Run }) {
  const text = run.error || run.status;
  if (!text) return <span />;
  const tone = run.error ? "text-red-400" : "text-[var(--muted)]";
  return (
    <div className={`min-w-0 truncate pl-4 font-mono text-[11px] ${tone}`}>
      <Tooltip content={text}>
        <span>{text}</span>
      </Tooltip>
    </div>
  );
}

function RunTimestampBlock({ run }: { run: Run }) {
  const sinceTs = run.finished_at || run.started_at;
  return (
    <Tooltip content={<RunSummaryTip run={run} />}>
      <span className="font-mono tabular-nums text-[11px] text-[var(--muted)] inline-flex items-center gap-1 shrink-0">
        {fmtDatePrefix(run.started_at) && (
          <span className="text-[var(--foreground)]">
            {fmtDatePrefix(run.started_at)}
          </span>
        )}
        <span className="text-[var(--foreground)]">
          {fmtClock(run.started_at)}
        </span>
        <span>→</span>
        {run.finished_at &&
          fmtDatePrefix(run.finished_at) &&
          fmtDatePrefix(run.finished_at) !== fmtDatePrefix(run.started_at) && (
            <span className="text-[var(--foreground)]">
              {fmtDatePrefix(run.finished_at)}
            </span>
          )}
        <span className="text-[var(--foreground)]">
          {run.finished_at ? fmtClock(run.finished_at) : "—"}
        </span>
        <span>· {fmtAgo(sinceTs)}</span>
      </span>
    </Tooltip>
  );
}

function RunDurationCell({ run }: { run: Run }) {
  const startedMs = new Date(run.started_at).getTime();
  const finishedMs = run.finished_at ? new Date(run.finished_at).getTime() : 0;
  const elapsedMs = (finishedMs || Date.now()) - startedMs;
  if (elapsedMs <= 0) return <span />;
  return (
    <Tooltip
      content={
        run.finished_at
          ? `Duration: ${fmtMs(elapsedMs)}`
          : `Running for ${fmtMs(elapsedMs)}`
      }
    >
      <span className="font-mono tabular-nums text-[11px] text-[var(--muted)] truncate">
        {fmtMs(elapsedMs)}
      </span>
    </Tooltip>
  );
}

function StatusPill({ status }: { status: string }) {
  const cls = statusClass(status);
  return (
    <span
      className={`inline-block px-1.5 py-0.5 rounded text-[10px] font-mono font-semibold uppercase ${cls}`}
    >
      {status}
    </span>
  );
}

function statusClass(status: string): string {
  switch (status) {
    case "success":
      return "bg-green-500/15 text-green-400";
    case "failed":
      return "bg-red-500/15 text-red-400";
    case "running":
      return "bg-indigo-500/15 text-indigo-400";
    case "cancelled":
      return "bg-amber-500/15 text-amber-400";
    default:
      return "bg-[var(--background)] text-[var(--muted)]";
  }
}

function KV({
  label,
  value,
  tip,
}: {
  label: string;
  value: string | number;
  tip?: string;
}) {
  const body = (
    <div className={tip ? "cursor-help" : undefined}>
      <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
        {label}
      </div>
      <div className="font-mono text-xs mt-0.5">{value}</div>
    </div>
  );
  return tip ? <Tooltip content={tip}>{body}</Tooltip> : body;
}

function Panel({ children }: { children: React.ReactNode }) {
  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-6 text-xs text-[var(--muted)]">
      {children}
    </div>
  );
}

function EmptyPanel({
  hasFilter,
  hasRegistry,
}: {
  hasFilter: boolean;
  hasRegistry: boolean;
}) {
  if (hasFilter) {
    return <Panel>No pipelines match the current filter.</Panel>;
  }
  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-6 text-xs text-[var(--muted)] space-y-2">
      <p>No pipelines to show yet.</p>
      {!hasRegistry && (
        <p>
          The pipelines registry is empty. That&apos;s expected for the prod
          dashboard pod, which runs outside a{" "}
          <code className="font-mono">.sparkwing</code> repo. Trigger a run from
          the CLI and it&apos;ll show up here.
        </p>
      )}
    </div>
  );
}

function Footer() {
  return (
    <div className="mt-4 pt-3 border-t border-[var(--border)] text-[10px] text-[var(--muted)] space-y-1">
      <p>
        Registry reads the nearest{" "}
        <code className="font-mono">.sparkwing/pipelines.yaml</code>; stats come
        from the last {RUNS_WINDOW} runs. Pipelines tagged{" "}
        <code className="font-mono">runs-only</code> have run at least once but
        aren&apos;t in the local registry (e.g. consumer-repo pipelines when
        viewing sparkwing&apos;s dashboard).
      </p>
      <p>
        Argument schemas are empty until compiled-binary introspection lands;
        the trigger form falls back to a free-text arg editor per pipeline.
      </p>
    </div>
  );
}

function TimeAgo({ ts }: { ts: string }) {
  const [, force] = useState(0);
  useEffect(() => {
    const i = setInterval(() => force((x) => x + 1), 1000);
    return () => clearInterval(i);
  }, []);
  const sec = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
  if (sec < 60) return <span>{sec}s ago</span>;
  if (sec < 3600) return <span>{Math.floor(sec / 60)}m ago</span>;
  if (sec < 86_400) return <span>{Math.floor(sec / 3600)}h ago</span>;
  return <span>{Math.floor(sec / 86_400)}d ago</span>;
}

function formatDuration(ms: number): string {
  if (!ms) return "-";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms - m * 60_000) / 1000);
  return `${m}m ${s}s`;
}
