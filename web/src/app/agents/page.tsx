"use client";

// Runner fleet panel (FOLLOWUPS #7). Shows every runner the controller
// has seen a claim from in the last hour: pool pods, laptop agents,
// and anything matching the same holder-prefix shape.
//
// Data source: /api/v1/agents (pkg/controller/agents.go). That
// endpoint walks the nodes table; it does NOT know about idle
// runners that haven't claimed yet. Until we add an explicit
// registration table, the empty state shows a hint for starting an
// agent or scaling the pool.

import { useCallback, useEffect, useMemo, useState } from "react";
import { type Agent, getAgents } from "@/lib/api";
import { HeartbeatLabel } from "@/components/HeartbeatDot";

const POLL_MS = 5000;

function relativeTime(iso: string): string {
  if (!iso) return "-";
  const age = (Date.now() - new Date(iso).getTime()) / 1000;
  if (Number.isNaN(age) || age < 0) return "-";
  if (age < 60) return `${Math.round(age)}s ago`;
  if (age < 3600) return `${Math.round(age / 60)}m ago`;
  return `${Math.round(age / 3600)}h ago`;
}

function typeBadge(kind: string): { label: string; cls: string } {
  switch (kind) {
    case "agent":
      return { label: "agent", cls: "bg-indigo-400/15 text-indigo-300" };
    case "pool":
      return { label: "pool", cls: "bg-emerald-400/15 text-emerald-300" };
    case "local":
      return { label: "local", cls: "bg-slate-400/15 text-slate-300" };
    default:
      return { label: kind || "?", cls: "bg-gray-400/15 text-gray-300" };
  }
}

// Sort rule: busy agents first, then by type (agent, pool, local,
// other), then name. Keeps the active fleet at the top.
function sortAgents(a: Agent, b: Agent): number {
  if (a.status !== b.status) return a.status === "busy" ? -1 : 1;
  if (a.type !== b.type) {
    const order = ["agent", "pool", "local"];
    const ai = order.indexOf(a.type);
    const bi = order.indexOf(b.type);
    return (ai < 0 ? 99 : ai) - (bi < 0 ? 99 : bi);
  }
  return a.name.localeCompare(b.name);
}

export default function AgentsPage() {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});

  const refresh = useCallback(async () => {
    const rows = await getAgents();
    setAgents(rows);
    setLoaded(true);
  }, []);

  useEffect(() => {
    refresh();
    const i = window.setInterval(() => {
      if (!document.hidden) refresh();
    }, POLL_MS);
    return () => window.clearInterval(i);
  }, [refresh]);

  const sorted = useMemo(() => [...agents].sort(sortAgents), [agents]);

  const totals = useMemo(() => {
    const byType: Record<string, number> = {};
    let busy = 0;
    let activeJobs = 0;
    for (const a of agents) {
      byType[a.type] = (byType[a.type] || 0) + 1;
      if (a.status === "busy") busy++;
      activeJobs += a.active_jobs?.length || 0;
    }
    return { total: agents.length, byType, busy, activeJobs };
  }, [agents]);

  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-6xl mx-auto w-full">
      <div className="flex items-baseline justify-between mb-4">
        <h1 className="text-xl font-bold">Fleet</h1>
        <span className="text-[10px] font-mono text-[var(--muted)]">
          /api/v1/agents - refresh every {POLL_MS / 1000}s
        </span>
      </div>

      <FleetSummary totals={totals} />

      {!loaded ? (
        <LoadingPanel />
      : sorted.length === 0 ? (
        <EmptyPanel />
      : (
        <div className="space-y-2">
          {sorted.map((a) => {
            const key = `${a.type}:${a.name}`;
            const isOpen = !!expanded[key];
            return (
              <AgentRow
                key={key}
                agent={a}
                expanded={isOpen}
                onToggle={() => setExpanded((e) => ({ ...e, [key]: !e[key] }))}
              />
            );
          })}
        </div>
      )}

      <FleetFooter />
    </div>
  );
}

function FleetSummary({
  totals,
}: {
  totals: {
    total: number;
    byType: Record<string, number>;
    busy: number;
    activeJobs: number;
  };
}) {
  const cards: Array<{ label: string; value: string | number }> = [
    { label: "runners seen (1h)", value: totals.total },
    { label: "busy now", value: totals.busy },
    { label: "in-flight claims", value: totals.activeJobs },
  ];
  const typeOrder = ["agent", "pool", "local"];
  for (const k of typeOrder) {
    if (totals.byType[k]) cards.push({ label: k, value: totals.byType[k] });
  }
  return (
    <div className="grid grid-cols-2 md:grid-cols-4 lg:grid-cols-6 gap-3 mb-4">
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

function AgentRow({
  agent,
  expanded,
  onToggle,
}: {
  agent: Agent;
  expanded: boolean;
  onToggle: () => void;
}) {
  const badge = typeBadge(agent.type);
  const active = agent.active_jobs?.length || 0;
  const labels = Object.entries(agent.labels || {});

  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg overflow-hidden">
      <button
        onClick={onToggle}
        className="w-full flex items-center gap-3 px-3 py-2.5 text-left hover:bg-[var(--surface-raised)] transition-colors"
      >
        <span className="w-4 text-center text-xs text-[var(--muted)]">
          {expanded ? "-" : "+"}
        </span>
        <span
          className={`px-1.5 py-0.5 rounded text-[10px] font-mono font-bold ${badge.cls}`}
        >
          {badge.label}
        </span>
        <span className="font-mono text-sm font-medium truncate flex-1">
          {agent.name || "(anonymous)"}
        </span>
        <StatusPill status={agent.status} />
        <span className="text-xs text-[var(--muted)] font-mono w-32 text-right">
          {active > 0
            ? `${active} claim${active === 1 ? "" : "s"}`
            : "no claims"}
        </span>
        <HeartbeatLabel lastHeartbeat={agent.last_seen} />
      </button>

      {expanded && (
        <div className="border-t border-[var(--border)] px-3 py-3 space-y-3 text-xs">
          <div className="grid grid-cols-2 gap-3">
            <KV label="type" value={agent.type} />
            <KV label="max concurrent" value={agent.max_concurrent || "-"} />
            <KV label="last seen" value={relativeTime(agent.last_seen)} />
            <KV label="status" value={agent.status} />
          </div>
          {labels.length > 0 && (
            <div>
              <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1">
                labels
              </div>
              <div className="flex flex-wrap gap-1">
                {labels.map(([k, v]) => (
                  <span
                    key={k}
                    className="font-mono text-[10px] px-1.5 py-0.5 bg-[var(--background)] border border-[var(--border)] rounded"
                  >
                    {v ? `${k}=${v}` : k}
                  </span>
                ))}
              </div>
            </div>
          )}
          {active > 0 && (
            <div>
              <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1">
                active claims
              </div>
              <div className="space-y-1">
                {agent.active_jobs!.map((runID) => (
                  <a
                    key={runID}
                    href={`/#/runs/${runID}`}
                    className="block font-mono text-xs text-[var(--accent)] hover:underline"
                  >
                    {runID}
                  </a>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function StatusPill({ status }: { status: string }) {
  const cls =
    status === "busy"
      ? "bg-indigo-400/15 text-indigo-300"
      : "bg-slate-400/15 text-slate-300";
  const dot =
    status === "busy" ? "bg-indigo-400 animate-pulse" : "bg-slate-500";
  return (
    <span
      className={`inline-flex items-center gap-1.5 px-1.5 py-0.5 rounded text-[10px] font-mono font-bold ${cls}`}
    >
      <span className={`w-1.5 h-1.5 rounded-full ${dot}`} />
      {status || "idle"}
    </span>
  );
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

function LoadingPanel() {
  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-6 text-xs text-[var(--muted)]">
      Loading fleet...
    </div>
  );
}

function EmptyPanel() {
  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-6 text-xs text-[var(--muted)] space-y-2">
      <p>
        No runners have claimed a node in the last hour. The controller derives
        fleet state from claim activity, so idle runners show up only when they
        take work.
      </p>
      <p>
        Start a laptop agent:{" "}
        <code className="bg-[var(--background)] px-1 py-0.5 rounded font-mono">
          sparkwing agent --config agent.yaml
        </code>
        , or confirm the cluster pool is running:{" "}
        <code className="bg-[var(--background)] px-1 py-0.5 rounded font-mono">
          kubectl -n sparkwing get deploy sparkwing-warm-runner
        </code>
        .
      </p>
    </div>
  );
}

function FleetFooter() {
  return (
    <div className="mt-4 pt-3 border-t border-[var(--border)] text-[10px] text-[var(--muted)] space-y-1">
      <p>
        Fleet is derived from the nodes table's claim activity over a 1-hour
        window. Idle runners without recent claims are invisible until an
        explicit registration/heartbeat table lands (FOLLOWUPS backlog).
      </p>
      <p>
        Max-concurrent + labels come back empty because runners do not advertise
        them to the controller yet; today they are local knobs on each{" "}
        <code className="font-mono">wing runner</code> /{" "}
        <code className="font-mono">sparkwing agent</code> process.
      </p>
    </div>
  );
}
