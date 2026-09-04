"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import {
  type Agent,
  type ServiceStatus,
  getAgents,
  getServiceHealth,
} from "@/lib/api";
import { HeartbeatLabel } from "@/components/HeartbeatDot";
import {
  fleetHeadroomState,
  fleetLocation,
  fleetRegistration,
  fleetSlotTotals,
  fleetSlots,
  formatFleetResources,
  sortFleetAgents,
} from "@/lib/fleet";

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

function typeBadge(kind: string): { label: string; cls: string } {
  switch (kind) {
    case "agent":
      return { label: "agent", cls: "bg-indigo-400/15 text-indigo-300" };
    case "gateway":
      return { label: "gateway", cls: "bg-cyan-400/15 text-cyan-300" };
    case "pool":
      return { label: "pool", cls: "bg-emerald-400/15 text-emerald-300" };
    case "local":
      return { label: "local", cls: "bg-slate-400/15 text-slate-300" };
    default:
      return { label: kind || "?", cls: "bg-gray-400/15 text-gray-300" };
  }
}

export default function ClusterPage() {
  const [services, setServices] = useState<ServiceStatus[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [expandedAgent, setExpandedAgent] = useState<Record<string, boolean>>(
    {},
  );

  const refresh = useCallback(async () => {
    const [svc, ag] = await Promise.all([getServiceHealth(), getAgents()]);
    setServices(svc);
    setAgents(ag);
    setLoaded(true);
  }, []);

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

  const sortedAgents = useMemo(
    () => [...agents].sort(sortFleetAgents),
    [agents],
  );

  const fleetTotals = useMemo(() => {
    const byType: Record<string, number> = {};
    let busy = 0;
    for (const a of agents) {
      byType[a.type] = (byType[a.type] || 0) + 1;
      if (a.status === "busy") busy++;
    }
    return {
      total: agents.length,
      byType,
      busy,
      activeSlots: fleetSlotTotals(agents).active,
    };
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
        <h1 className="text-xl font-bold">Fleet</h1>
        <span className="text-[10px] font-mono text-[var(--muted)]">
          refresh every {POLL_MS / 1000}s
        </span>
      </div>

      <OverallCard
        status={overall}
        services={services.length}
        fleet={fleetTotals.total}
        busy={fleetTotals.busy}
      />

      <SectionHeader title="Services" hint="/api/v1/health/services" />
      <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-4 mb-6">
        {!loaded ? (
          <div className="text-xs text-[var(--muted)]">Loading...</div>
        ) : services.length === 0 ? (
          <div className="text-xs text-[var(--muted)]">
            No services configured. Pass --controller and --logs to
            sparkwing-web to populate this list.
          </div>
        ) : (
          <div className="space-y-3">
            {services.map((svc) => (
              <ServiceRow key={svc.name} svc={svc} maxLatency={maxLatency} />
            ))}
          </div>
        )}
      </div>

      <SectionHeader title="Fleet" hint="/api/v1/agents" />
      <FleetCards totals={fleetTotals} />
      <div className="space-y-2 mb-6">
        {!loaded ? (
          <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-4 text-xs text-[var(--muted)]">
            Loading fleet...
          </div>
        ) : sortedAgents.length === 0 ? (
          <FleetEmpty />
        ) : (
          sortedAgents.map((a) => {
            const key = `${a.type}:${a.name}`;
            return (
              <AgentRow
                key={key}
                agent={a}
                expanded={!!expandedAgent[key]}
                onToggle={() =>
                  setExpandedAgent((e) => ({ ...e, [key]: !e[key] }))
                }
              />
            );
          })
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
}: {
  status: string;
  services: number;
  fleet: number;
  busy: number;
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
      <div className="grid grid-cols-3 gap-3">
        <Stat label="Services probed" value={services} />
        <Stat label="Executors" value={fleet} />
        <Stat label="Busy executors" value={busy} />
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
    activeSlots: number | null;
  };
}) {
  const cards: Array<{ label: string; value: number | null }> = [
    { label: "total", value: totals.total },
    { label: "busy", value: totals.busy },
    { label: "active slots", value: totals.activeSlots },
  ];
  const typeOrder = ["agent", "gateway", "pool", "local"];
  for (const k of typeOrder) {
    if (totals.byType[k]) cards.push({ label: k, value: totals.byType[k] });
  }
  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-4 mb-3">
      <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-3">
        {cards.map((c) => (
          <div
            key={c.label}
            className="bg-[var(--background)] border border-[var(--border)] rounded px-3 py-2"
          >
            <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
              {c.label}
            </div>
            <div className="text-lg font-mono mt-0.5">{c.value ?? "-"}</div>
          </div>
        ))}
      </div>
    </div>
  );
}

function FleetEmpty() {
  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-6 text-xs text-[var(--muted)] space-y-2">
      <p>
        No executors are registered and no legacy runners have claimed a node in
        the last hour.
      </p>
      <p>
        Start a laptop agent:{" "}
        <code className="bg-[var(--background)] px-1 py-0.5 rounded font-mono">
          sparkwing-runner agent --config agent.yaml
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
  const activeRuns = agent.active_jobs?.length || 0;
  const registration = fleetRegistration(agent);
  const headroomState = fleetHeadroomState(agent);
  const labels = Object.entries(agent.labels || {});

  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg overflow-hidden">
      <button
        onClick={onToggle}
        aria-expanded={expanded}
        className="w-full flex items-center gap-3 px-3 py-2.5 text-left hover:bg-[var(--surface-raised)] transition-colors"
      >
        <span
          aria-hidden
          className="w-4 text-center text-xs text-[var(--muted)]"
        >
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
        <span className="hidden md:flex flex-col items-end gap-0.5">
          <span className="text-[9px] uppercase tracking-wider text-[var(--muted)]">
            Policy
          </span>
          {registration === "registered" ? (
            <LocationPill location={fleetLocation(agent)} configured />
          ) : (
            <span className="text-[10px] font-mono text-[var(--muted)]">
              legacy activity
            </span>
          )}
        </span>
        <span className="hidden lg:flex flex-col items-end gap-0.5">
          <span className="text-[9px] uppercase tracking-wider text-[var(--muted)]">
            Liveness
          </span>
          <AgentStatusPill status={agent.status} />
        </span>
        <span className="hidden xl:flex flex-col items-end gap-0.5 text-[10px] font-mono">
          <span className="text-[9px] uppercase tracking-wider text-[var(--muted)]">
            Activity
          </span>
          <span>
            {fleetSlots(agent)} slots · {activeRuns} runs
          </span>
        </span>
        <span className="hidden 2xl:inline">
          <HeartbeatLabel lastHeartbeat={agent.last_seen} />
        </span>
      </button>

      {expanded && (
        <div className="border-t border-[var(--border)] px-3 py-3 grid gap-3 lg:grid-cols-3 text-xs">
          <FleetDetailSection title="Configured policy">
            {registration === "legacy" ? (
              <p className="text-[var(--muted)]">
                Configuration unavailable. This executor was inferred from
                recent activity.
              </p>
            ) : (
              <>
                <div className="grid grid-cols-2 gap-3">
                  <KV label="type" value={agent.type || "unknown"} />
                  <KV label="location" value={fleetLocation(agent)} />
                  <KV
                    label="priority"
                    value={
                      agent.base_priority == null
                        ? "unknown"
                        : `${agent.base_priority} (ceiling ${agent.priority_ceiling ?? "unknown"})`
                    }
                  />
                  <KV label="slot limit" value={agent.max_concurrent} />
                  <KV
                    label="contribution budget"
                    value={formatFleetResources(agent.budget, true)}
                  />
                </div>
                <div>
                  <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1">
                    capabilities
                  </div>
                  {labels.length === 0 ? (
                    <span className="text-[var(--muted)]">None configured</span>
                  ) : (
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
                  )}
                </div>
              </>
            )}
          </FleetDetailSection>

          <FleetDetailSection title="Observed liveness">
            <div className="grid grid-cols-2 gap-3">
              <KV label="status" value={agent.status || "unknown"} />
              <KV
                label="last heartbeat"
                value={relativeTime(agent.last_seen)}
              />
              <KV
                label="headroom"
                value={
                  headroomState === "reported"
                    ? formatFleetResources(agent.headroom)
                    : headroomState === "stale"
                      ? "stale or unavailable"
                      : "not reported"
                }
              />
              {agent.headroom && (
                <>
                  <KV
                    label="headroom observed"
                    value={relativeTime(agent.headroom.observed_at)}
                  />
                  <KV
                    label="admission queue"
                    value={agent.headroom.queue_depth}
                  />
                </>
              )}
            </div>
            <p className="text-[10px] text-[var(--muted)]">
              {headroomState === "reported"
                ? "The timestamp records when the controller accepted this headroom measurement."
                : headroomState === "stale"
                  ? "The controller omits headroom after its observation expires."
                  : "The controller has no live headroom observation for this executor."}
            </p>
          </FleetDetailSection>

          <FleetDetailSection title="Current activity">
            <div className="grid grid-cols-2 gap-3">
              <KV label="active slots" value={agent.active_slots ?? "unknown"} />
              <KV
                label="slot capacity"
                value={agent.max_concurrent > 0 ? agent.max_concurrent : "unknown"}
              />
              <KV label="active runs" value={activeRuns} />
            </div>
            {activeRuns > 0 ? (
              <div className="space-y-1">
                {agent.active_jobs!.map((runID) => (
                  <Link
                    key={runID}
                    href={`/runs?run=${encodeURIComponent(runID)}`}
                    className="block font-mono text-xs text-[var(--accent)] hover:underline"
                  >
                    {runID}
                  </Link>
                ))}
              </div>
            ) : (
              <p className="text-[var(--muted)]">No active runs.</p>
            )}
          </FleetDetailSection>
        </div>
      )}
    </div>
  );
}

function AgentStatusPill({ status }: { status: string }) {
  const cls =
    status === "busy"
      ? "bg-indigo-400/15 text-indigo-300"
      : status === "offline"
        ? "bg-red-400/15 text-red-300"
        : "bg-slate-400/15 text-slate-300";
  const dot =
    status === "busy"
      ? "bg-indigo-400 animate-pulse"
      : status === "offline"
        ? "bg-red-400"
        : "bg-slate-500";
  return (
    <span
      className={`inline-flex items-center gap-1.5 px-1.5 py-0.5 rounded text-[10px] font-mono font-bold ${cls}`}
    >
      <span aria-hidden className={`w-1.5 h-1.5 rounded-full ${dot}`} />
      {status || "idle"}
    </span>
  );
}

function LocationPill({
  location,
  configured = false,
}: {
  location: string;
  configured?: boolean;
}) {
  const cls =
    location === "local"
      ? "bg-emerald-400/15 text-emerald-300"
      : location === "cloud"
        ? "bg-sky-400/15 text-sky-300"
        : "bg-slate-400/15 text-slate-300";
  return (
    <span
      className={`rounded px-1.5 py-0.5 text-[10px] font-mono font-bold ${cls}`}
      aria-label={`${configured ? "Configured" : "Observed"} location: ${location}`}
    >
      {location}
    </span>
  );
}

function FleetDetailSection({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <section
      aria-label={title}
      className="rounded border border-[var(--border)] bg-[var(--background)] p-3 space-y-3"
    >
      <h3 className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
        {title}
      </h3>
      {children}
    </section>
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
