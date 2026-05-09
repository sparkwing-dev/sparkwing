"use client";

// Pipelines registry view. Lists every pipeline discovered from
// /api/v1/pipelines and annotates each with recent-run stats pulled
// from /api/runs. Clicking a row expands recent runs (links into the
// run detail on /) and a TriggerForm pre-filled with the pipeline
// name. In the prod dashboard pod the registry endpoint returns
// empty -- we fall back to pipelines derived from the runs list.

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  type PipelineMeta,
  type Run,
  getPipelines,
  getRuns,
  runDurationMs,
} from "@/lib/api";
import TriggerForm from "@/components/TriggerForm";

const POLL_MS = 5000;
const RUNS_WINDOW = 200;
const SPARK_SIZE = 12;

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

export default function PipelinesPage() {
  const [registry, setRegistry] = useState<Record<string, PipelineMeta>>({});
  const [runs, setRuns] = useState<Run[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [filter, setFilter] = useState("");
  const [expanded, setExpanded] = useState<string | null>(null);
  const [triggerOpen, setTriggerOpen] = useState<string | null>(null);

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

  const rows = useMemo(
    () => buildRows(registry, runs).sort(sortRows),
    [registry, runs],
  );

  const filtered = useMemo(() => {
    if (!filter.trim()) return rows;
    const q = filter.toLowerCase();
    return rows.filter(
      (row) =>
        row.pipeline.toLowerCase().includes(q) ||
        (row.repo?.toLowerCase().includes(q) ?? false) ||
        (row.meta?.tags || []).some((t) => t.toLowerCase().includes(q)),
    );
  }, [rows, filter]);

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
    <div className="flex-1 overflow-y-auto p-6 max-w-6xl mx-auto w-full">
      <div className="flex items-baseline justify-between mb-4">
        <h1 className="text-xl font-bold">Pipelines</h1>
        <span className="text-[10px] font-mono text-[var(--muted)]">
          /api/v1/pipelines + /api/runs - refresh every {POLL_MS / 1000}s
        </span>
      </div>

      <SummaryCards totals={totals} />

      <div className="flex items-center gap-2 mb-3">
        <input
          type="search"
          placeholder="filter by pipeline or tag"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          className="flex-1 bg-[var(--background)] border border-[var(--border)] rounded px-3 py-1.5 text-sm"
        />
        <button
          onClick={() => refresh()}
          className="text-xs text-[var(--muted)] hover:text-[var(--foreground)] border border-[var(--border)] rounded px-2 py-1.5"
          title="refresh"
        >
          refresh
        </button>
      </div>

      {!loaded ? (
        <Panel>Loading pipelines...</Panel>
      ) : filtered.length === 0 ? (
        <EmptyPanel
          hasFilter={!!filter.trim()}
          hasRegistry={totals.registered > 0}
        />
      ) : (
        <div className="space-y-2">
          {filtered.map((row) => (
            <PipelineCard
              key={row.key}
              row={row}
              expanded={expanded === row.key}
              onToggle={() =>
                setExpanded((cur) => (cur === row.key ? null : row.key))
              }
              triggerOpen={triggerOpen === row.key}
              onTrigger={() =>
                setTriggerOpen((cur) => (cur === row.key ? null : row.key))
              }
              onTriggered={() => {
                setTriggerOpen(null);
                refresh();
              }}
            />
          ))}
        </div>
      )}

      <Footer />
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
    { label: "pipelines", value: totals.pipelines },
    { label: "registered", value: totals.registered },
    { label: `runs (last ${RUNS_WINDOW})`, value: totals.runs },
    { label: "passed", value: totals.passed },
    { label: "failed", value: totals.failed },
    { label: "running", value: totals.running },
  ];
  return (
    <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-3 mb-4">
      {cards.map((c) => (
        <div
          key={c.label}
          className="bg-[var(--surface)] border border-[var(--border)] rounded-lg px-3 py-2"
        >
          <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
            {c.label}
          </div>
          <div className="text-lg font-mono mt-0.5">{c.value}</div>
        </div>
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
}: {
  row: PipelineRow;
  expanded: boolean;
  onToggle: () => void;
  triggerOpen: boolean;
  onTrigger: () => void;
  onTriggered: () => void;
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
              <span className="text-cyan-400/80">{row.repo}</span>
              <span className="text-[var(--muted)] mx-1">/</span>
            </>
          )}
          <span className="text-violet-300">{row.pipeline}</span>
        </span>
        {!row.meta && (
          <span
            className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-amber-400/15 text-amber-300"
            title="not in the local pipelines.yaml registry"
          >
            runs-only
          </span>
        )}
        {tags.map((t) => (
          <span
            key={t}
            className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-[var(--background)] text-[var(--muted)]"
          >
            {t}
          </span>
        ))}
        <Sparkline runs={recent} />
        <span className="text-xs text-[var(--muted)] font-mono w-20 text-right">
          {successRate === null ? "-" : `${successRate}%`}
        </span>
        <span className="text-xs text-[var(--muted)] font-mono w-24 text-right">
          {stats.total} run{stats.total === 1 ? "" : "s"}
        </span>
        <span className="text-xs text-[var(--muted)] font-mono w-24 text-right">
          {row.lastRun ? <TimeAgo ts={row.lastRun.started_at} /> : "never run"}
        </span>
      </button>

      {expanded && (
        <div className="border-t border-[var(--border)] px-3 py-3 space-y-3 text-xs">
          <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
            <KV label="passed" value={stats.passed} />
            <KV label="failed" value={stats.failed} />
            <KV label="running" value={stats.running} />
            <KV
              label="avg duration"
              value={row.avgDurMs ? formatDuration(row.avgDurMs) : "-"}
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

          <RecentRuns runs={row.runs.slice(0, 15)} />
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
        <span
          key={r.id}
          title={`${r.status} - ${r.id}`}
          className={`block w-1.5 h-3 rounded-sm ${sparkColor(r.status)}`}
        />
      ))}
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

function RecentRuns({ runs }: { runs: Run[] }) {
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
        {runs.map((r) => (
          <li key={r.id} className="px-2 py-1.5 flex items-center gap-2">
            <span
              className={`w-1.5 h-1.5 rounded-full shrink-0 ${sparkColor(r.status)}`}
            />
            <a
              href={`/runs?run=${r.id}`}
              className="font-mono text-xs text-[var(--accent)] hover:underline truncate min-w-0 flex-1"
            >
              {r.id}
            </a>
            {r.git_branch && (
              <span className="text-[11px] text-amber-400/70 font-mono shrink-0 truncate max-w-[160px]">
                ⎇ {r.git_branch}
              </span>
            )}
            {r.git_sha && (
              <span className="text-[11px] text-[var(--muted)] font-mono shrink-0">
                {r.git_sha.slice(0, 7)}
              </span>
            )}
            <StatusPill status={r.status} />
            <span className="text-[11px] text-[var(--muted)] font-mono w-20 text-right shrink-0 tabular-nums">
              {r.status === "running"
                ? "running"
                : formatDuration(runDurationMs(r))}
            </span>
            <span className="text-[11px] text-[var(--muted)] font-mono w-24 text-right shrink-0">
              <TimeAgo ts={r.started_at} />
            </span>
          </li>
        ))}
      </ul>
    </div>
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

function KV({ label, value }: { label: string; value: string | number }) {
  return (
    <div>
      <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
        {label}
      </div>
      <div className="font-mono text-xs mt-0.5">{value}</div>
    </div>
  );
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
