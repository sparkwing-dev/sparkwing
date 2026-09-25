"use client";


import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import {
  type Approval,
  type Run,
  getPendingApprovals,
  getRuns,
} from "@/lib/api";
import {
  type Metric,
  ANCHOR_OPTIONS,
  DEFAULT_ANCHOR_MS,
  WEEK_MS,
  deltaAbs,
  deltaPct,
  summarize,
} from "@/lib/overview";
import {
  fmtAgo,
  fmtDateTime,
  fmtFullDate,
  fmtMsCompact,
} from "@/lib/timeFormat";
import Tooltip from "@/components/Tooltip";
import { Sparkline } from "@/components/PipelineOverview";
import { recentPipelineTriage } from "@/lib/homeTriage";

const POLL_MS = 15000;
const OVERVIEW_RUN_LIMIT = 1000;

export default function Home() {
  const [runs, setRuns] = useState<Run[]>([]);
  const [approvals, setApprovals] = useState<Approval[]>([]);
  const [anchorMs, setAnchorMs] = useState(DEFAULT_ANCHOR_MS);
  const [loaded, setLoaded] = useState(false);
  const [includeFeatureBranches, setIncludeFeatureBranches] = useState(false);
  const [now, setNow] = useState(() => Date.now());

  const refresh = useCallback(async () => {
    const sinceHrs = Math.ceil((anchorMs + WEEK_MS) / (60 * 60 * 1000)) + 24;
    const [rs, ap] = await Promise.all([
      getRuns({ since: `${sinceHrs}h`, limit: OVERVIEW_RUN_LIMIT }),
      getPendingApprovals(),
    ]);
    setRuns(rs);
    setApprovals(ap);
    setNow(Date.now());
    setLoaded(true);
  }, [anchorMs]);

  useEffect(() => {
    let cancelled = false;
    queueMicrotask(() => {
      if (!cancelled) void refresh();
    });
    const i = window.setInterval(() => {
      if (!document.hidden) refresh();
    }, POLL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(i);
    };
  }, [refresh]);

  const overview = useMemo(
    () => summarize(runs, now, anchorMs),
    [runs, now, anchorMs],
  );

  const running = useMemo(
    () => runs.filter((r) => r.status === "running"),
    [runs],
  );

  const { failed: failedPipelines, recovered: recoveredPipelines } = useMemo(
    () => recentPipelineTriage(runs, includeFeatureBranches),
    [runs, includeFeatureBranches],
  );

  const anchorLabel =
    ANCHOR_OPTIONS.find((o) => o.ms === anchorMs)?.label ?? "anchor";

  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-5xl mx-auto w-full">
      <div className="flex items-baseline justify-between mb-1">
        <h1 className="text-xl font-bold">Overview</h1>
        <span className="text-[10px] font-mono text-[var(--muted)]">
          refresh every {POLL_MS / 1000}s
        </span>
      </div>
      <div className="flex items-center gap-2 mb-5">
        <span className="text-xs text-[var(--muted)]">compared to</span>
        <AnchorSelect value={anchorMs} onChange={setAnchorMs} />
      </div>

      {!loaded ? (
        <div role="status" aria-label="Loading overview" className="animate-pulse motion-reduce:animate-none">
          <div className="grid grid-cols-2 lg:grid-cols-4 gap-3 mb-5">
            {Array.from({ length: 4 }, (_, index) => (
              <div key={index} className="h-24 bg-[var(--surface)] border border-[var(--border)] rounded-lg" />
            ))}
          </div>
          <div className="h-14 bg-[var(--surface)] border border-[var(--border)] rounded-lg mb-5" />
          <div className="h-20 bg-[var(--surface)] border border-[var(--border)] rounded-lg" />
        </div>
      ) : (
        <>
          {runs.length >= OVERVIEW_RUN_LIMIT && (
            <p role="status" className="mb-3 text-xs text-[var(--muted)]">
              Metrics use the latest {OVERVIEW_RUN_LIMIT} runs and may exclude older history.
            </p>
          )}
          <div className="grid grid-cols-2 lg:grid-cols-4 gap-3 mb-5">
            <MetricCard metric={overview.buildTime} anchorLabel={anchorLabel} />
            <MetricCard metric={overview.deploys1d} anchorLabel={anchorLabel} />
            <MetricCard metric={overview.deploys7d} anchorLabel={anchorLabel} />
            <MetricCard
              metric={overview.successRate}
              anchorLabel={anchorLabel}
            />
          </div>

          {runs.length < 5 && <GettingStarted />}

          <NeedsAttention
            approvals={approvals}
            running={running.length}
          />

          <section className="mb-5">
            <div className="flex flex-wrap items-center justify-between gap-2 mb-2">
              <h2 className="text-xs font-bold uppercase tracking-wider text-[var(--muted)]">
                Recently Failed Pipelines <span className="font-normal normal-case tracking-normal">({includeFeatureBranches ? "all branches" : "default branch"})</span>
              </h2>
              <label className="flex items-center gap-2 text-xs text-[var(--muted)] cursor-pointer">
                <input
                  type="checkbox"
                  checked={includeFeatureBranches}
                  onChange={(event) => setIncludeFeatureBranches(event.target.checked)}
                  className="accent-violet-500"
                />
                All branches
              </label>
            </div>
            {failedPipelines.length === 0 ? (
              <Panel><span className="text-sm text-[var(--muted)]">No recently failed pipelines.</span></Panel>
            ) : (
              <div className="space-y-2">
                {failedPipelines.map(({ key, repo, pipeline, branch, latest, runs: history }) => (
                  <Link
                    key={key}
                    href={`/runs?run=${encodeURIComponent(latest.id)}`}
                    className="block rounded-lg border border-red-500/30 bg-[var(--surface)] px-3 py-2 hover:bg-[var(--surface-raised)]"
                  >
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                      <span className="min-w-0 flex-1 truncate font-mono text-sm text-violet-300" title={`${repo}/${pipeline}`}>
                        {repo.replace(/^github\.com\//, "")} / {pipeline}{branch ? ` · ${branch}` : ""}
                      </span>
                      <Sparkline runs={history.slice(0, 30)} />
                      <span className="text-[11px] font-mono text-[var(--muted)]">
                        {fmtAgo(latest.started_at)}
                        {now - Date.parse(latest.started_at) > WEEK_MS ? " · older than 7d" : ""}
                      </span>
                    </div>
                    <div className="mt-1 truncate font-mono text-[11px] text-red-300" title={latest.error || ""}>
                      {latest.error || "Failed without a recorded error"}
                    </div>
                  </Link>
                ))}
              </div>
            )}
          </section>

          <details className="mb-5 group">
            <summary className="cursor-pointer text-xs font-bold uppercase tracking-wider text-[var(--muted)] mb-2">
              <h2 className="inline">Recently Recovered Pipelines <span className="font-normal normal-case tracking-normal">({includeFeatureBranches ? "all branches" : "default branch"})</span></h2>
              <span className="ml-2 font-mono font-normal">{recoveredPipelines.length}</span>
            </summary>
            {recoveredPipelines.length === 0 ? (
              <Panel><span className="text-sm text-[var(--muted)]">No recently recovered pipelines.</span></Panel>
            ) : (
              <div className="space-y-2">
                {recoveredPipelines.map(({ key, repo, pipeline, branch, latest, runs: history }) => (
                  <Link
                    key={key}
                    href={`/runs?run=${encodeURIComponent(latest.id)}`}
                    className="block rounded-lg border border-green-500/30 bg-[var(--surface)] px-3 py-2 hover:bg-[var(--surface-raised)]"
                  >
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                      <span className="min-w-0 flex-1 truncate font-mono text-sm text-violet-300" title={`${repo}/${pipeline}`}>
                        {repo.replace(/^github\.com\//, "")} / {pipeline}{branch ? ` · ${branch}` : ""}
                      </span>
                      <Sparkline runs={history.slice(0, 30)} />
                      <span className="text-[11px] font-mono text-green-400">passed</span>
                      <span className="text-[11px] font-mono text-[var(--muted)]">{fmtAgo(latest.started_at)}</span>
                    </div>
                  </Link>
                ))}
              </div>
            )}
          </details>

          <LastDeployCard run={overview.lastDeploy} />
        </>
      )}
    </div>
  );
}

function AnchorSelect({
  value,
  onChange,
}: {
  value: number;
  onChange: (ms: number) => void;
}) {
  return (
    <div className="inline-flex rounded-md border border-[var(--border)] overflow-hidden">
      {ANCHOR_OPTIONS.map((o) => (
        <button
          key={o.ms}
          type="button"
          onClick={() => onChange(o.ms)}
          className={`px-2.5 py-1 text-xs font-mono transition-colors ${
            o.ms === value
              ? "bg-[var(--surface-raised)] text-[var(--foreground)]"
              : "bg-[var(--surface)] text-[var(--muted)] hover:bg-[var(--surface-raised)]"
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

function formatValue(metric: Metric, v: number | null): string {
  if (v == null) return "--";
  if (metric.unit === "ms") return fmtMsCompact(v);
  if (metric.unit === "pct") return `${Math.round(v * 100)}%`;
  return String(v);
}

function MetricCard({
  metric,
  anchorLabel,
}: {
  metric: Metric;
  anchorLabel: string;
}) {
  const abs = deltaAbs(metric);
  const pct = deltaPct(metric);
  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg px-4 py-3">
      <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
        {metric.label}
      </div>
      <div className="text-2xl font-mono mt-1 text-[var(--foreground)]">
        {formatValue(metric, metric.current)}
      </div>
      <DeltaChip
        metric={metric}
        abs={abs}
        pct={pct}
        anchorLabel={anchorLabel}
      />
    </div>
  );
}

function DeltaChip({
  metric,
  abs,
  pct,
  anchorLabel,
}: {
  metric: Metric;
  abs: number | null;
  pct: number | null;
  anchorLabel: string;
}) {
  if (abs == null || metric.previous == null) {
    return (
      <div className="text-[11px] font-mono text-[var(--muted)] mt-1">
        no {anchorLabel} baseline
      </div>
    );
  }
  if (abs === 0) {
    return (
      <div className="text-[11px] font-mono text-[var(--muted)] mt-1">
        unchanged vs {anchorLabel}
      </div>
    );
  }
  const improved = metric.higherIsBetter ? abs > 0 : abs < 0;
  const color = improved ? "text-green-400" : "text-red-400";
  const arrow = abs > 0 ? "▲" : "▼";
  const magnitude =
    pct != null
      ? `${Math.abs(Math.round(pct * 100))}%`
      : formatValue(metric, Math.abs(abs));
  return (
    <Tooltip content={`${formatValue(metric, metric.previous)} ${anchorLabel}`}>
      <div className={`text-[11px] font-mono mt-1 ${color} cursor-default`}>
        {arrow} {magnitude} vs {anchorLabel}
      </div>
    </Tooltip>
  );
}

function LastDeployCard({ run }: { run: Run | null }) {
  if (!run) {
    return (
      <Panel>
        <span className="text-sm text-[var(--muted)]">
          No completed deploys yet.
        </span>
      </Panel>
    );
  }
  const ok = run.status === "success";
  const ts = run.finished_at || run.started_at;
  return (
    <Link
      href={`/runs?run=${run.id}`}
      className={`flex items-center gap-3 bg-[var(--surface)] border rounded-lg px-4 py-3 mb-5 hover:bg-[var(--surface-raised)] transition-colors ${
        ok ? "border-[var(--border)]" : "border-red-500/50"
      }`}
    >
      <span
        className={`w-2.5 h-2.5 rounded-full shrink-0 ${
          ok ? "bg-green-400" : "bg-red-400"
        }`}
      />
      <div className="min-w-0 flex-1">
        <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
          Last deploy
        </div>
        <div className="flex items-center gap-2 mt-0.5">
          <span className="font-mono text-sm text-violet-300 truncate">
            {run.pipeline}
          </span>
          {run.git_branch && (
            <span className="text-[11px] text-amber-400/70 font-mono truncate max-w-[160px]">
              ⎇ {run.git_branch}
            </span>
          )}
        </div>
      </div>
      <span
        className={`text-xs font-mono shrink-0 ${
          ok ? "text-green-400" : "text-red-400"
        }`}
      >
        {ok ? "passed" : "failed"}
      </span>
      <Tooltip content={fmtFullDate(ts)}>
        <span className="text-[11px] font-mono text-[var(--muted)] shrink-0 cursor-default">
          {fmtDateTime(ts)} · {fmtAgo(ts)}
        </span>
      </Tooltip>
    </Link>
  );
}

function GettingStarted() {
  return (
    <section className="mb-5 rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4">
      <h2 className="text-sm font-semibold">Get started</h2>
      <ol className="mt-2 list-decimal list-inside space-y-1 text-sm text-[var(--muted)]">
        <li>Add a pipeline to your repository.</li>
        <li><Link href="/team/machines" className="text-indigo-300 underline">Connect a runner</Link> on your machine or in GitHub Actions.</li>
        <li>Trigger a run.</li>
        <li>Follow its result in Runs.</li>
      </ol>
      <div className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-sm text-indigo-300">
        <a href="https://sparkwing.dev/docs/" target="_blank" rel="noopener noreferrer" className="hover:underline">
          Setup docs ↗
        </a>
        <Link href="/runs" className="hover:underline">Browse runs →</Link>
      </div>
    </section>
  );
}

function NeedsAttention({
  approvals,
  running,
}: {
  approvals: Approval[];
  running: number;
}) {
  const nothing = approvals.length === 0;
  return (
    <div className="mb-6">
      <div className="flex items-baseline gap-2 mb-2">
        <h2 className="text-xs font-bold uppercase tracking-wider text-[var(--muted)]">
          Needs attention
        </h2>
        {running > 0 && (
          <Link
            href="/runs"
            className="text-[11px] font-mono text-indigo-300 hover:underline"
          >
            {running} in flight
          </Link>
        )}
      </div>
      {nothing ? (
        <Panel>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="flex items-center gap-2 text-sm">
              No pending approvals.
            </span>
            <Link href="/runs" className="text-sm text-indigo-300 hover:underline">
              Browse runs →
            </Link>
          </div>
        </Panel>
      ) : (
        <ul className="bg-[var(--surface)] border border-[var(--border)] rounded-lg overflow-hidden divide-y divide-[var(--border)]">
          {approvals.map((a) => (
            <li key={`${a.run_id}/${a.node_id}`}>
              <Link
                href={`/runs?run=${a.run_id}&node=${encodeURIComponent(a.node_id)}`}
                className="flex items-center gap-3 px-3 py-2 hover:bg-[var(--surface-raised)] transition-colors"
              >
                <span className="w-2 h-2 rounded-full bg-yellow-400 animate-pulse shrink-0" />
                <span className="text-[11px] font-mono text-amber-400 shrink-0">
                  approval
                </span>
                <span className="font-mono text-xs truncate flex-1">
                  {a.node_id}
                </span>
                {

                                                                       }
                <Tooltip content={fmtFullDate(a.requested_at)}>
                  <span className="text-[11px] font-mono text-[var(--muted)] shrink-0 cursor-default">
                    {fmtDateTime(a.requested_at)} · {fmtAgo(a.requested_at)}
                  </span>
                </Tooltip>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Panel({ children }: { children: React.ReactNode }) {
  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-4 mb-5">
      {children}
    </div>
  );
}
