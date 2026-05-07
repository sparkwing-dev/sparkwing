"use client";

// Cluster status view. Consolidates what used to live under the
// "Service Health" toggle on /, plus a fleet summary and a recent
// failures list, into one dedicated page.
//
// Data sources (all existing):
//   /api/v1/health/services - controller + logs (+ any ExtraServices)
//   /api/v1/agents          - runners seen in the last hour
//   /api/runs?limit=50      - recent runs, filtered to failed here
//
// Cache, dind, and pool probes are not wired into ExtraServices today;
// when sparkwing-web grows a flag for that they'll appear here without
// any UI change.

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import {
  type Agent,
  type Run,
  type ServiceStatus,
  getAgents,
  getRuns,
  getServiceHealth,
} from "@/lib/api";

const POLL_MS = 5000;

function statusColor(status: string): string {
  if (status === "ok") return "bg-green-400";
  if (status === "degraded") return "bg-amber-400";
  if (status === "down") return "bg-red-400";
  return "bg-gray-400";
}

function statusText(status: string): string {
  if (status === "ok") return "Healthy";
  if (status === "degraded") return "Degraded";
  if (status === "down") return "Down";
  return "Unknown";
}

function latencyColor(ms: number): string {
  if (ms < 50) return "bg-green-400/60";
  if (ms < 200) return "bg-amber-400/60";
  return "bg-red-400/60";
}

function LatencyBar({ ms, max }: { ms: number; max: number }) {
  const w = max > 0 ? Math.min(100, Math.round((ms / max) * 100)) : 0;
  return (
    <div className="flex items-center gap-2">
      <div className="flex-1 bg-[var(--background)] rounded-full h-1.5 overflow-hidden">
        <div
          className={`h-full rounded-full ${latencyColor(ms)}`}
          style={{ width: `${w}%` }}
        />
      </div>
      <span className="text-[10px] font-mono text-[var(--muted)] w-12 text-right shrink-0">
        {ms}ms
      </span>
    </div>
  );
}

function relativeTime(iso: string): string {
  if (!iso) return "-";
  const age = (Date.now() - new Date(iso).getTime()) / 1000;
  if (Number.isNaN(age) || age < 0) return "-";
  if (age < 60) return `${Math.round(age)}s ago`;
  if (age < 3600) return `${Math.round(age / 60)}m ago`;
  if (age < 86400) return `${Math.round(age / 3600)}h ago`;
  return `${Math.round(age / 86400)}d ago`;
}

export default function ClusterPage() {
  const [services, setServices] = useState<ServiceStatus[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [failures, setFailures] = useState<Run[]>([]);
  const [loaded, setLoaded] = useState(false);

  const refresh = useCallback(async () => {
    const [svc, ag, runs] = await Promise.all([
      getServiceHealth(),
      getAgents(),
      getRuns({ limit: 50 }),
    ]);
    setServices(svc);
    setAgents(ag);
    setFailures(runs.filter((r) => r.status === "failed").slice(0, 15));
    setLoaded(true);
  }, []);

  useEffect(() => {
    refresh();
    const i = window.setInterval(() => {
      if (!document.hidden) refresh();
    }, POLL_MS);
    return () => window.clearInterval(i);
  }, [refresh]);

  const fleetTotals = useMemo(() => {
    const byType: Record<string, number> = {};
    let busy = 0;
    let claims = 0;
    for (const a of agents) {
      byType[a.type] = (byType[a.type] || 0) + 1;
      if (a.status === "busy") busy++;
      claims += a.active_jobs?.length || 0;
    }
    return { total: agents.length, byType, busy, claims };
  }, [agents]);

  const maxLatency = Math.max(1, ...services.map((s) => s.latency_ms));
  const overall =
    services.length === 0
      ? "unknown"
      : services.every((s) => s.status === "ok")
        ? "ok"
        : services.some((s) => s.status === "down")
          ? "down"
          : "degraded";

  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-6xl mx-auto w-full">
      <div className="flex items-baseline justify-between mb-4">
        <h1 className="text-xl font-bold">Cluster</h1>
        <span className="text-[10px] font-mono text-[var(--muted)]">
          refresh every {POLL_MS / 1000}s
        </span>
      </div>

      <OverallCard
        status={overall}
        services={services.length}
        fleet={fleetTotals.total}
        busy={fleetTotals.busy}
        recentFailures={failures.length}
      />

      <SectionHeader title="Services" hint="/api/v1/health/services" />
      <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-4 mb-6">
        {!loaded ? (
          <div className="text-xs text-[var(--muted)]">Loading...</div>
        : services.length === 0 ? (
          <div className="text-xs text-[var(--muted)]">
            No services configured. Pass --controller and --logs to
            sparkwing-web to populate this list.
          </div>
        : (
          <div className="space-y-3">
            {services.map((svc) => (
              <ServiceRow key={svc.name} svc={svc} maxLatency={maxLatency} />
            ))}
          </div>
        )}
      </div>

      <SectionHeader title="Fleet" hint="/api/v1/agents - last hour" />
      <FleetCards totals={fleetTotals} />

      <SectionHeader title="Recent failures" hint="/api/runs status=failed" />
      <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg overflow-hidden mb-6">
        {!loaded ? (
          <div className="text-xs text-[var(--muted)] p-4">Loading...</div>
        : failures.length === 0 ? (
          <div className="text-xs text-[var(--muted)] p-4">
            No failed runs in the latest 50.
          </div>
        : (
          <table className="w-full text-xs">
            <thead className="bg-[var(--background)] text-[var(--muted)]">
              <tr>
                <th className="text-left px-3 py-2 font-medium">Pipeline</th>
                <th className="text-left px-3 py-2 font-medium">Run</th>
                <th className="text-left px-3 py-2 font-medium">When</th>
                <th className="text-left px-3 py-2 font-medium">Error</th>
              </tr>
            </thead>
            <tbody>
              {failures.map((r) => (
                <tr
                  key={r.id}
                  className="border-t border-[var(--border)] hover:bg-[var(--surface-raised)]"
                >
                  <td className="px-3 py-1.5 font-mono">{r.pipeline}</td>
                  <td className="px-3 py-1.5 font-mono">
                    <Link
                      href={`/#/runs/${r.id}`}
                      className="text-[var(--accent)] hover:underline"
                    >
                      {r.id.slice(-8)}
                    </Link>
                  </td>
                  <td className="px-3 py-1.5 text-[var(--muted)]">
                    {relativeTime(r.finished_at || r.started_at)}
                  </td>
                  <td className="px-3 py-1.5 text-red-400 truncate max-w-md">
                    {r.error || "(no message)"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

function SectionHeader({ title, hint }: { title: string; hint: string }) {
  return (
    <div className="flex items-baseline justify-between mb-2 mt-4">
      <h2 className="text-xs font-bold uppercase tracking-wider text-[var(--muted)]">
        {title}
      </h2>
      <span className="text-[10px] font-mono text-[var(--muted)]">{hint}</span>
    </div>
  );
}

function OverallCard({
  status,
  services,
  fleet,
  busy,
  recentFailures,
}: {
  status: string;
  services: number;
  fleet: number;
  busy: number;
  recentFailures: number;
}) {
  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-4 mb-4">
      <div className="flex items-center gap-3 mb-3">
        <div
          className={`w-3 h-3 rounded-full ${statusColor(status)} ${
            status !== "ok" ? "animate-pulse" : ""
          }`}
        />
        <span className="text-sm font-medium">
          {status === "ok"
            ? "All systems operational"
            : status === "degraded"
              ? "Degraded - at least one service is slow or partial"
              : status === "down"
                ? "Down - at least one service is unreachable"
                : "Status unknown"}
        </span>
      </div>
      <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
        <Stat label="Services probed" value={services} />
        <Stat label="Runners (1h)" value={fleet} />
        <Stat label="Busy runners" value={busy} />
        <Stat
          label="Recent failures"
          value={recentFailures}
          warn={recentFailures > 0}
        />
      </div>
    </div>
  );
}

function Stat({
  label,
  value,
  warn,
}: {
  label: string;
  value: number;
  warn?: boolean;
}) {
  return (
    <div className="bg-[var(--background)] border border-[var(--border)] rounded px-3 py-2">
      <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
        {label}
      </div>
      <div
        className={`text-lg font-mono mt-0.5 ${
          warn ? "text-red-400" : "text-[var(--foreground)]"
        }`}
      >
        {value}
      </div>
    </div>
  );
}

function ServiceRow({
  svc,
  maxLatency,
}: {
  svc: ServiceStatus;
  maxLatency: number;
}) {
  return (
    <div>
      <div className="flex items-center gap-3">
        <div
          className={`w-2 h-2 rounded-full shrink-0 ${statusColor(svc.status)}`}
        />
        <span className="text-xs text-[var(--foreground)] w-24 shrink-0 font-mono">
          {svc.name}
        </span>
        <span
          className={`text-[10px] w-16 shrink-0 ${
            svc.status === "ok"
              ? "text-green-400"
              : svc.status === "down"
                ? "text-red-400"
                : "text-amber-400"
          }`}
        >
          {statusText(svc.status)}
        </span>
        <div className="flex-1 min-w-32">
          <LatencyBar ms={svc.latency_ms} max={maxLatency} />
        </div>
        <span
          className="text-[10px] text-[var(--muted)] font-mono truncate max-w-xs"
          title={svc.url}
        >
          {svc.url}
        </span>
      </div>
      {svc.error && (
        <div className="ml-5 mt-1 text-[10px] text-red-400 font-mono">
          {svc.error}
        </div>
      )}
      {svc.problems && svc.problems.length > 0 && (
        <div className="ml-5 mt-1 space-y-0.5">
          {svc.problems.map((msg, i) => (
            <div
              key={i}
              className="text-[10px] text-amber-400/80 leading-tight font-mono"
            >
              {msg}
            </div>
          ))}
        </div>
      )}
      <div className="ml-5 mt-0.5 text-[10px] text-[var(--muted)]">
        last check: {relativeTime(svc.checked_at)}
      </div>
    </div>
  );
}

function FleetCards({
  totals,
}: {
  totals: {
    total: number;
    byType: Record<string, number>;
    busy: number;
    claims: number;
  };
}) {
  const cards: Array<{ label: string; value: number }> = [
    { label: "total", value: totals.total },
    { label: "busy", value: totals.busy },
    { label: "in-flight claims", value: totals.claims },
  ];
  const typeOrder = ["agent", "pool", "local"];
  for (const k of typeOrder) {
    if (totals.byType[k]) cards.push({ label: k, value: totals.byType[k] });
  }
  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-4 mb-6">
      <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-3">
        {cards.map((c) => (
          <div
            key={c.label}
            className="bg-[var(--background)] border border-[var(--border)] rounded px-3 py-2"
          >
            <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
              {c.label}
            </div>
            <div className="text-lg font-mono mt-0.5">{c.value}</div>
          </div>
        ))}
      </div>
      <div className="mt-3 text-[10px] text-[var(--muted)]">
        See{" "}
        <Link href="/agents" className="text-[var(--accent)] hover:underline">
          /agents
        </Link>{" "}
        for per-runner detail.
      </div>
    </div>
  );
}
