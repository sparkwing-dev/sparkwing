"use client";

// Home: executive summary. Answers "is delivery healthy and trending the
// right way?" at a glance -- current build time, deploy volume, success
// rate, and the last deploy, each compared against an adjustable anchor.
// Resolved past failures are deliberately absent: they are not
// actionable. Chronological build/failure detail lives on Runs; long-term
// trends live on Analytics. The only historical signal kept here is a red
// last deploy, because that is current state worth acting on.

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import {
  type Approval,
  type Run,
  type ServiceStatus,
  getPendingApprovals,
  getRuns,
  getServiceHealth,
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
import { fmtAgo, fmtFullDate, fmtMsCompact } from "@/lib/timeFormat";
import Tooltip from "@/components/Tooltip";

const POLL_MS = 15000;

export default function Home() {
  const [runs, setRuns] = useState<Run[]>([]);
  const [approvals, setApprovals] = useState<Approval[]>([]);
  const [services, setServices] = useState<ServiceStatus[]>([]);
  const [anchorMs, setAnchorMs] = useState(DEFAULT_ANCHOR_MS);
  const [loaded, setLoaded] = useState(false);
  const [now, setNow] = useState(() => Date.now());

  const refresh = useCallback(async () => {
    // Fetch enough history to cover the longest metric window (7d) sitting
    // behind the anchor, plus a margin.
    const sinceHrs = Math.ceil((anchorMs + WEEK_MS) / (60 * 60 * 1000)) + 24;
    const [rs, ap, svc] = await Promise.all([
      getRuns({ since: `${sinceHrs}h`, limit: 2000 }),
      getPendingApprovals(),
      getServiceHealth(),
    ]);
    setRuns(rs);
    setApprovals(ap);
    setServices(svc);
    setNow(Date.now());
    setLoaded(true);
  }, [anchorMs]);

  useEffect(() => {
    refresh();
    const i = window.setInterval(() => {
      if (!document.hidden) refresh();
    }, POLL_MS);
    return () => window.clearInterval(i);
  }, [refresh]);

  const overview = useMemo(
    () => summarize(runs, now, anchorMs),
    [runs, now, anchorMs],
  );

  const degraded = useMemo(
    () => services.filter((s) => s.status !== "ok"),
    [services],
  );

  const running = useMemo(
    () => runs.filter((r) => r.status === "running"),
    [runs],
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
        <Panel>Loading...</Panel>
      ) : (
        <>
          <div className="grid grid-cols-2 lg:grid-cols-4 gap-3 mb-5">
            <MetricCard metric={overview.buildTime} anchorLabel={anchorLabel} />
            <MetricCard metric={overview.deploys1d} anchorLabel={anchorLabel} />
            <MetricCard metric={overview.deploys7d} anchorLabel={anchorLabel} />
            <MetricCard
              metric={overview.successRate}
              anchorLabel={anchorLabel}
            />
          </div>

          <LastDeployCard run={overview.lastDeploy} />

          <NeedsAttention
            approvals={approvals}
            degraded={degraded}
            running={running.length}
          />
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
          {fmtAgo(ts)}
        </span>
      </Tooltip>
    </Link>
  );
}

function NeedsAttention({
  approvals,
  degraded,
  running,
}: {
  approvals: Approval[];
  degraded: ServiceStatus[];
  running: number;
}) {
  const nothing = approvals.length === 0 && degraded.length === 0;
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
          <div className="flex items-center gap-2">
            <span className="w-2 h-2 rounded-full bg-green-400" />
            <span className="text-sm">
              Nothing needs attention. Services healthy, no pending approvals.
            </span>
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
                <Tooltip content={fmtFullDate(a.requested_at)}>
                  <span className="text-[11px] font-mono text-[var(--muted)] shrink-0 cursor-default">
                    {fmtAgo(a.requested_at)}
                  </span>
                </Tooltip>
              </Link>
            </li>
          ))}
          {degraded.map((s) => (
            <li key={s.name}>
              <Link
                href="/cluster"
                className="flex items-center gap-3 px-3 py-2 hover:bg-[var(--surface-raised)] transition-colors"
              >
                <span
                  className={`w-2 h-2 rounded-full shrink-0 ${
                    s.status === "down" ? "bg-red-400" : "bg-amber-400"
                  }`}
                />
                <span
                  className={`text-[11px] font-mono shrink-0 ${
                    s.status === "down" ? "text-red-400" : "text-amber-400"
                  }`}
                >
                  {s.status}
                </span>
                <span className="font-mono text-xs truncate flex-1">
                  {s.name}
                </span>
                <span className="text-[11px] font-mono text-[var(--muted)] shrink-0 tabular-nums">
                  {s.latency_ms}ms
                </span>
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
