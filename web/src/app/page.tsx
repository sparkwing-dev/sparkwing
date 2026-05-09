"use client";

// Home: triage view. Answers "what should I look at right now?" —
// distinct from Runs (chronological feed) and Analytics (long-term
// trends). Surfaces approvals, recent failures, and degraded
// infrastructure on one glanceable screen.

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

const POLL_MS = 5000;
const FAILURE_WINDOW_MS = 24 * 60 * 60 * 1000;

export default function Home() {
  const [approvals, setApprovals] = useState<Approval[]>([]);
  const [runs, setRuns] = useState<Run[]>([]);
  const [services, setServices] = useState<ServiceStatus[]>([]);
  const [loaded, setLoaded] = useState(false);

  const refresh = useCallback(async () => {
    const [ap, rs, svc] = await Promise.all([
      getPendingApprovals(),
      getRuns({ limit: 100 }),
      getServiceHealth(),
    ]);
    setApprovals(ap);
    setRuns(rs);
    setServices(svc);
    setLoaded(true);
  }, []);

  useEffect(() => {
    refresh();
    const i = window.setInterval(() => {
      if (!document.hidden) refresh();
    }, POLL_MS);
    return () => window.clearInterval(i);
  }, [refresh]);

  const recentFailures = useMemo(() => {
    const cutoff = Date.now() - FAILURE_WINDOW_MS;
    return runs
      .filter((r) => r.status === "failed")
      .filter((r) => {
        const ts = r.finished_at || r.started_at;
        return ts && new Date(ts).getTime() >= cutoff;
      })
      .slice(0, 20);
  }, [runs]);

  const running = useMemo(
    () => runs.filter((r) => r.status === "running"),
    [runs],
  );

  const degraded = useMemo(
    () => services.filter((s) => s.status !== "ok"),
    [services],
  );

  const nothingToTriage =
    loaded &&
    approvals.length === 0 &&
    recentFailures.length === 0 &&
    degraded.length === 0;

  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-5xl mx-auto w-full">
      <div className="flex items-baseline justify-between mb-4">
        <h1 className="text-xl font-bold">Now</h1>
        <span className="text-[10px] font-mono text-[var(--muted)]">
          refresh every {POLL_MS / 1000}s · 24h window
        </span>
      </div>

      <StatusStrip
        approvals={approvals.length}
        failures={recentFailures.length}
        degraded={degraded.length}
        running={running.length}
      />

      {!loaded ? (
        <Panel>Loading...</Panel>
      ) : nothingToTriage ? (
        <Panel>
          <div className="flex items-center gap-2">
            <span className="w-2 h-2 rounded-full bg-green-400" />
            <span className="text-sm">
              Nothing needs attention right now. Services healthy, no recent
              failures, no pending approvals.
            </span>
          </div>
        </Panel>
      ) : (
        <>
          {approvals.length > 0 && (
            <Section
              title="Pending approvals"
              count={approvals.length}
              tone="warn"
            >
              <ApprovalList approvals={approvals} />
            </Section>
          )}

          {degraded.length > 0 && (
            <Section
              title="Cluster degradations"
              count={degraded.length}
              tone="warn"
            >
              <DegradedList services={degraded} />
            </Section>
          )}

          {recentFailures.length > 0 && (
            <Section
              title="Recent failures (last 24h)"
              count={recentFailures.length}
              tone="bad"
            >
              <FailureList runs={recentFailures} />
            </Section>
          )}
        </>
      )}

      {running.length > 0 && (
        <Section title="In flight" count={running.length}>
          <RunningList runs={running} />
        </Section>
      )}
    </div>
  );
}

function StatusStrip({
  approvals,
  failures,
  degraded,
  running,
}: {
  approvals: number;
  failures: number;
  degraded: number;
  running: number;
}) {
  const items: Array<{
    label: string;
    value: number;
    href: string;
    tone: "warn" | "bad" | "ok" | "info";
  }> = [
    {
      label: "approvals",
      value: approvals,
      href: "/runs",
      tone: approvals > 0 ? "warn" : "ok",
    },
    {
      label: "failures (24h)",
      value: failures,
      href: "/runs",
      tone: failures > 0 ? "bad" : "ok",
    },
    {
      label: "degraded services",
      value: degraded,
      href: "/cluster",
      tone: degraded > 0 ? "bad" : "ok",
    },
    {
      label: "running",
      value: running,
      href: "/runs",
      tone: "info",
    },
  ];
  const toneCls: Record<string, string> = {
    warn: "text-amber-400",
    bad: "text-red-400",
    ok: "text-green-400",
    info: "text-indigo-300",
  };
  return (
    <div className="grid grid-cols-2 md:grid-cols-4 gap-3 mb-6">
      {items.map((it) => (
        <Link
          key={it.label}
          href={it.href}
          className="bg-[var(--surface)] border border-[var(--border)] rounded-lg px-3 py-2 hover:bg-[var(--surface-raised)] transition-colors"
        >
          <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
            {it.label}
          </div>
          <div
            className={`text-2xl font-mono mt-0.5 ${
              it.value > 0 ? toneCls[it.tone] : "text-[var(--foreground)]"
            }`}
          >
            {it.value}
          </div>
        </Link>
      ))}
    </div>
  );
}

function Section({
  title,
  count,
  tone,
  children,
}: {
  title: string;
  count?: number;
  tone?: "warn" | "bad";
  children: React.ReactNode;
}) {
  const toneCls =
    tone === "bad"
      ? "text-red-400"
      : tone === "warn"
        ? "text-amber-400"
        : "text-[var(--muted)]";
  return (
    <div className="mb-6">
      <div className="flex items-baseline gap-2 mb-2">
        <h2 className="text-xs font-bold uppercase tracking-wider text-[var(--muted)]">
          {title}
        </h2>
        {count !== undefined && (
          <span className={`text-xs font-mono ${toneCls}`}>{count}</span>
        )}
      </div>
      {children}
    </div>
  );
}

function Panel({ children }: { children: React.ReactNode }) {
  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-4">
      {children}
    </div>
  );
}

function ApprovalList({ approvals }: { approvals: Approval[] }) {
  return (
    <ul className="bg-[var(--surface)] border border-[var(--border)] rounded-lg overflow-hidden divide-y divide-[var(--border)]">
      {approvals.map((a) => (
        <li key={`${a.run_id}/${a.node_id}`}>
          <Link
            href={`/runs?run=${a.run_id}&node=${encodeURIComponent(a.node_id)}`}
            className="flex items-center gap-3 px-3 py-2 hover:bg-[var(--surface-raised)] transition-colors"
          >
            <span className="w-2 h-2 rounded-full bg-yellow-400 animate-pulse shrink-0" />
            <span className="font-mono text-xs font-medium truncate flex-1">
              {a.node_id}
            </span>
            <span className="text-[11px] font-mono text-[var(--muted)] truncate max-w-md">
              {a.message || a.run_id}
            </span>
            <span className="text-[11px] font-mono text-[var(--muted)] shrink-0">
              <RelTime ts={a.requested_at} />
            </span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

function FailureList({ runs }: { runs: Run[] }) {
  return (
    <ul className="bg-[var(--surface)] border border-[var(--border)] rounded-lg overflow-hidden divide-y divide-[var(--border)]">
      {runs.map((r) => (
        <li key={r.id}>
          <Link
            href={`/runs?run=${r.id}`}
            className="flex items-center gap-3 px-3 py-2 hover:bg-[var(--surface-raised)] transition-colors"
          >
            <span className="w-2 h-2 rounded-full bg-red-400 shrink-0" />
            <span className="font-mono text-xs text-violet-300 shrink-0">
              {r.pipeline}
            </span>
            {r.git_branch && (
              <span className="text-[11px] text-amber-400/70 font-mono shrink-0 truncate max-w-[140px]">
                ⎇ {r.git_branch}
              </span>
            )}
            <span className="text-[11px] text-red-400 font-mono truncate flex-1 min-w-0">
              {r.error || "(no error message)"}
            </span>
            <span className="text-[11px] font-mono text-[var(--muted)] shrink-0">
              <RelTime ts={r.finished_at || r.started_at} />
            </span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

function DegradedList({ services }: { services: ServiceStatus[] }) {
  return (
    <ul className="bg-[var(--surface)] border border-[var(--border)] rounded-lg overflow-hidden divide-y divide-[var(--border)]">
      {services.map((s) => (
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
            <span className="font-mono text-xs font-medium shrink-0">
              {s.name}
            </span>
            <span
              className={`text-[11px] font-mono shrink-0 ${
                s.status === "down" ? "text-red-400" : "text-amber-400"
              }`}
            >
              {s.status}
            </span>
            <span className="text-[11px] font-mono text-[var(--muted)] truncate flex-1 min-w-0">
              {s.error || s.url}
            </span>
            <span className="text-[11px] font-mono text-[var(--muted)] shrink-0 tabular-nums">
              {s.latency_ms}ms
            </span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

function RunningList({ runs }: { runs: Run[] }) {
  return (
    <ul className="bg-[var(--surface)] border border-[var(--border)] rounded-lg overflow-hidden divide-y divide-[var(--border)]">
      {runs.slice(0, 10).map((r) => (
        <li key={r.id}>
          <Link
            href={`/runs?run=${r.id}`}
            className="flex items-center gap-3 px-3 py-2 hover:bg-[var(--surface-raised)] transition-colors"
          >
            <span className="w-2 h-2 rounded-full bg-indigo-400 animate-pulse shrink-0" />
            <span className="font-mono text-xs text-violet-300 shrink-0">
              {r.pipeline}
            </span>
            {r.git_branch && (
              <span className="text-[11px] text-amber-400/70 font-mono shrink-0 truncate max-w-[140px]">
                ⎇ {r.git_branch}
              </span>
            )}
            <span className="ml-auto text-[11px] font-mono text-[var(--muted)] shrink-0">
              started <RelTime ts={r.started_at} />
            </span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

function RelTime({ ts }: { ts: string }) {
  if (!ts) return <span>-</span>;
  const age = (Date.now() - new Date(ts).getTime()) / 1000;
  if (Number.isNaN(age) || age < 0) return <span>-</span>;
  if (age < 60) return <span>{Math.round(age)}s ago</span>;
  if (age < 3600) return <span>{Math.round(age / 60)}m ago</span>;
  if (age < 86400) return <span>{Math.round(age / 3600)}h ago</span>;
  return <span>{Math.round(age / 86400)}d ago</span>;
}
