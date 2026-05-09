"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  type Run,
  type RunDetail,
  cancelRun,
  computeVenue,
  getNodeLogs,
  getNodeStreamUrl,
  getRun,
  getRuns,
  parseHolder,
  runDurationMs,
} from "@/lib/api";
import { ansiToHtml } from "@/lib/ansi";
import TriggerForm from "@/components/TriggerForm";
import ServiceHealth from "@/components/ServiceHealth";
import TrendCharts from "@/components/TrendCharts";
import LogSearch from "@/components/LogSearch";
import MetricsPanel from "@/components/MetricsPanel";
import ExecutionWaterfall from "@/components/ExecutionWaterfall";
import ResourceChart from "@/components/ResourceChart";

// Poll cadence matches the vanilla dashboard. Browser pauses when the
// tab is hidden; resume on focus.
const POLL_MS = 1500;

export default function Dashboard() {
  const [runs, setRuns] = useState<Run[]>([]);
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [detail, setDetail] = useState<RunDetail | null>(null);
  const [filter, setFilter] = useState("");
  const [showTrigger, setShowTrigger] = useState(false);
  const [showMetrics, setShowMetrics] = useState(false);
  const [showTrends, setShowTrends] = useState(false);
  const [showHealth, setShowHealth] = useState(false);
  const [showLogSearch, setShowLogSearch] = useState(false);
  const [logsNode, setLogsNode] = useState<string | null>(null);
  const venueCacheRef = useRef<Map<string, { status: string; venue: string }>>(
    new Map(),
  );

  // --- Data load ---

  const loadRuns = useCallback(async () => {
    const list = await getRuns({ limit: 50 });
    setRuns(list);
    // Background-fetch nodes for each run to compute the venue tag.
    // Terminal runs cache forever; running runs refetch each tick.
    await Promise.all(
      list.map(async (r) => {
        const cached = venueCacheRef.current.get(r.id);
        if (cached && cached.status === r.status && r.status !== "running") {
          return;
        }
        try {
          const d = await getRun(r.id);
          if (d) {
            venueCacheRef.current.set(r.id, {
              status: r.status,
              venue: computeVenue(d.nodes),
            });
          }
        } catch {
          // leave cache entry in place
        }
      }),
    );
    setRuns((prev) => [...prev]); // rerender for venue badges
  }, []);

  const loadDetail = useCallback(async (runID: string) => {
    const d = await getRun(runID);
    if (d) setDetail(d);
  }, []);

  useEffect(() => {
    loadRuns();
    const tick = () => {
      if (document.hidden) return;
      loadRuns();
      if (selectedID) loadDetail(selectedID);
    };
    const i = window.setInterval(tick, POLL_MS);
    return () => window.clearInterval(i);
  }, [loadRuns, loadDetail, selectedID]);

  useEffect(() => {
    if (selectedID) loadDetail(selectedID);
    else setDetail(null);
  }, [selectedID, loadDetail]);

  // --- Deep-link via #/runs/<id> ---

  useEffect(() => {
    const fromHash = () => {
      const m = window.location.hash.match(/^#\/runs\/([A-Za-z0-9-]+)$/);
      if (m) setSelectedID(m[1]);
      else setSelectedID(null);
    };
    fromHash();
    window.addEventListener("hashchange", fromHash);
    return () => window.removeEventListener("hashchange", fromHash);
  }, []);

  const selectRun = (id: string | null) => {
    setSelectedID(id);
    if (id) window.history.replaceState(null, "", `#/runs/${id}`);
    else window.history.replaceState(null, "", "#/");
  };

  const filteredRuns = useMemo(() => {
    if (!filter.trim()) return runs;
    const q = filter.toLowerCase();
    return runs.filter(
      (r) =>
        r.id.toLowerCase().includes(q) || r.pipeline.toLowerCase().includes(q),
    );
  }, [runs, filter]);

  const stats = useMemo(() => {
    const total = runs.length;
    const running = runs.filter((r) => r.status === "running").length;
    const passed = runs.filter((r) => r.status === "success").length;
    const failed = runs.filter((r) => r.status === "failed").length;
    return { total, running, passed, failed };
  }, [runs]);

  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-7xl mx-auto w-full">
      {/* Stats row */}
      <div className="grid grid-cols-4 gap-4 mb-6">
        <StatCard value={stats.total} label="Total runs" />
        <StatCard
          value={stats.running}
          label="Running"
          color="text-indigo-400"
        />
        <StatCard value={stats.passed} label="Passed" color="text-green-400" />
        <StatCard value={stats.failed} label="Failed" color="text-red-400" />
      </div>

      {/* Collapsible observability panels. Dark until their backing
          endpoints land (sessions B/E/G/H). */}
      <CollapsibleSection
        label="Metrics"
        expanded={showMetrics}
        onToggle={() => setShowMetrics(!showMetrics)}
      >
        <MetricsPanel jobs={[]} agents={[]} />
      </CollapsibleSection>

      <CollapsibleSection
        label="Trends"
        expanded={showTrends}
        onToggle={() => setShowTrends(!showTrends)}
      >
        <TrendCharts />
      </CollapsibleSection>

      <div className="flex gap-4 mb-6">
        <div className="flex-1">
          <CollapsibleSection
            label="Service Health"
            expanded={showHealth}
            onToggle={() => setShowHealth(!showHealth)}
            noMargin
          >
            <ServiceHealth />
          </CollapsibleSection>
        </div>
        <div className="flex-1">
          <CollapsibleSection
            label="Log Search"
            expanded={showLogSearch}
            onToggle={() => setShowLogSearch(!showLogSearch)}
            noMargin
          >
            <LogSearch />
          </CollapsibleSection>
        </div>
      </div>

      <div className="grid grid-cols-[380px_1fr] gap-6 min-h-[500px]">
        {/* Runs list */}
        <section className="bg-[var(--surface)] border border-[var(--border)] rounded-lg overflow-hidden">
          <div className="px-4 py-3 border-b border-[var(--border)] flex items-center gap-2">
            <h2 className="text-xs font-bold uppercase tracking-wider text-[var(--muted)]">
              Runs
            </h2>
            <input
              type="search"
              placeholder="filter by pipeline or id"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              className="flex-1 min-w-0 bg-[var(--background)] border border-[var(--border)] rounded px-2 py-1 text-xs"
            />
            <button
              onClick={() => loadRuns()}
              className="text-xs text-[var(--muted)] hover:text-[var(--foreground)] border border-[var(--border)] rounded px-2 py-1"
              title="refresh"
            >
              ↻
            </button>
            <button
              onClick={() => setShowTrigger(!showTrigger)}
              className="text-xs bg-green-500/20 text-green-400 border border-green-500/30 rounded px-2 py-1 font-medium hover:bg-green-500/30"
            >
              + Run
            </button>
          </div>
          {showTrigger && (
            <div className="p-3 border-b border-[var(--border)]">
              <TriggerForm
                onTriggered={() => {
                  loadRuns();
                  setShowTrigger(false);
                }}
                onClose={() => setShowTrigger(false)}
              />
            </div>
          )}
          <ul className="divide-y divide-[var(--border)]">
            {filteredRuns.length === 0 ? (
              <li className="px-4 py-6 text-center text-xs text-[var(--muted)]">
                {runs.length === 0
                  ? "no runs yet — click + Run or trigger from CLI"
                  : "no runs match filter"}
              </li>
            ) : (
              filteredRuns.map((r) => (
                <RunRow
                  key={r.id}
                  run={r}
                  venue={venueCacheRef.current.get(r.id)?.venue}
                  selected={r.id === selectedID}
                  onClick={() => selectRun(r.id)}
                />
              ))
            )}
          </ul>
        </section>

        {/* Run detail */}
        <section className="bg-[var(--surface)] border border-[var(--border)] rounded-lg overflow-hidden flex flex-col">
          {!selectedID || !detail ? (
            <div className="flex-1 flex items-center justify-center text-sm text-[var(--muted)]">
              Select a run to inspect its DAG and logs
            </div>
          ) : (
            <RunDetailView
              detail={detail}
              logsNode={logsNode}
              onSelectNode={(id) => setLogsNode(id)}
              onCloseLogs={() => setLogsNode(null)}
              onCancel={async () => {
                if (!confirm(`Cancel run ${detail.run.id}?`)) return;
                await cancelRun(detail.run.id);
                await loadDetail(detail.run.id);
              }}
            />
          )}
        </section>
      </div>
    </div>
  );
}

// --- Components ---

function StatCard({
  value,
  label,
  color,
}: {
  value: number;
  label: string;
  color?: string;
}) {
  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-4">
      <div className={`text-3xl font-bold ${color || ""}`}>{value}</div>
      <div className="text-xs text-[var(--muted)]">{label}</div>
    </div>
  );
}

function CollapsibleSection({
  label,
  expanded,
  onToggle,
  noMargin,
  children,
}: {
  label: string;
  expanded: boolean;
  onToggle: () => void;
  noMargin?: boolean;
  children: React.ReactNode;
}) {
  return (
    <div className={noMargin ? "" : "mb-6"}>
      <button
        onClick={onToggle}
        className="text-xs text-[var(--muted)] hover:text-[var(--foreground)] transition-colors flex items-center gap-1 mb-2"
      >
        <span>{expanded ? "▾" : "▸"}</span>
        <span>{label}</span>
      </button>
      {expanded && children}
    </div>
  );
}

function RunRow({
  run,
  venue,
  selected,
  onClick,
}: {
  run: Run;
  venue?: string;
  selected: boolean;
  onClick: () => void;
}) {
  return (
    <li
      onClick={onClick}
      className={`px-4 py-3 cursor-pointer border-l-2 ${statusBorder(
        run.status,
      )} ${selected ? "bg-[var(--surface-raised,var(--background))]" : "hover:bg-[var(--surface-raised,var(--background))]"}`}
    >
      <div className="flex items-center gap-2 mb-1">
        <span className="font-medium text-sm truncate flex-1">
          {run.pipeline}
        </span>
        <StatusPill status={run.status} />
      </div>
      <div className="flex items-center justify-between text-[11px] text-[var(--muted)]">
        <TimeAgo ts={run.started_at} />
        <span>
          {run.status === "running"
            ? "running"
            : formatDuration(runDurationMs(run))}
        </span>
      </div>
      {/* TODO: pipeline-tag chips removed with the legacy
          /api/runs list shape; restore once pipeline metadata is
          plumbed through the canonical /api/v1/runs response. */}
      {venue && (
        <div className="flex flex-wrap gap-1 mt-1.5">
          <VenueBadge venue={venue} />
        </div>
      )}
    </li>
  );
}

function statusBorder(status: string): string {
  switch (status) {
    case "running":
      return "border-indigo-400";
    case "success":
      return "border-green-400";
    case "failed":
      return "border-red-400";
    case "cancelled":
      return "border-amber-400";
    default:
      return "border-[var(--border)]";
  }
}

function StatusPill({ status }: { status: string }) {
  const cls = statusClass(status);
  return (
    <span
      className={`inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold uppercase tracking-wide ${cls}`}
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
    case "cached":
      return "bg-violet-500/15 text-violet-400";
    case "skipped":
      return "bg-slate-500/15 text-slate-400";
    case "skipped-concurrent":
      return "bg-slate-600/20 text-slate-300";
    case "superseded":
      return "bg-amber-600/15 text-amber-400";
    case "satisfied":
      return "bg-cyan-500/15 text-cyan-400";
    default:
      return "bg-[var(--background)] text-[var(--muted)]";
  }
}

function VenueBadge({ venue }: { venue: string }) {
  const color = venueClass(venue);
  return (
    <span
      className={`text-[10px] px-1.5 py-0.5 rounded font-mono font-semibold ${color}`}
      title={venueTooltip(venue)}
    >
      {venue}
    </span>
  );
}

function venueClass(venue: string): string {
  switch (venue) {
    case "pool":
      return "bg-indigo-500/15 text-indigo-300";
    case "jobs":
      return "bg-violet-500/15 text-violet-300";
    case "pool+jobs":
      return "bg-amber-500/15 text-amber-300";
    case "cluster":
      return "bg-cyan-500/15 text-cyan-300";
    default:
      return "bg-slate-500/15 text-slate-400";
  }
}

function venueTooltip(venue: string): string {
  switch (venue) {
    case "local":
      return "executed on this laptop";
    case "pool":
      return "executed by warm runner pool";
    case "jobs":
      return "executed as per-node K8s Jobs";
    case "pool+jobs":
      return "warm pool with K8sRunner fallback";
    default:
      return "executed on cluster runners";
  }
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
  if (!ms) return "—";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms - m * 60_000) / 1000);
  return `${m}m ${s}s`;
}

// --- Run detail view ---

function RunDetailView({
  detail,
  logsNode,
  onSelectNode,
  onCloseLogs,
  onCancel,
}: {
  detail: RunDetail;
  logsNode: string | null;
  onSelectNode: (id: string) => void;
  onCloseLogs: () => void;
  onCancel: () => Promise<void>;
}) {
  const { run, nodes } = detail;
  const layers = useMemo(() => computeLayers(nodes), [nodes]);
  const done = nodes.filter((n) => n.status === "done").length;

  return (
    <>
      <div className="px-4 py-3 border-b border-[var(--border)] flex items-center gap-3">
        <h2 className="font-mono text-sm truncate">
          {run.pipeline} — <span className="text-[var(--muted)]">{run.id}</span>
        </h2>
        <div className="flex-1" />
        <MetaLine run={run} done={done} total={nodes.length} />
        {run.status === "running" && (
          <button
            onClick={onCancel}
            className="text-xs border border-red-500/50 text-red-400 rounded px-2 py-1 hover:bg-red-500/20"
          >
            cancel
          </button>
        )}
      </div>
      <div className="flex-1 overflow-auto p-4 space-y-4">
        <Dag layers={layers} nodes={nodes} onClick={onSelectNode} />
        <ExecutionWaterfall run={run} nodes={nodes} />
      </div>
      {logsNode && (
        <LogDrawer
          runID={run.id}
          nodeID={logsNode}
          onClose={onCloseLogs}
          node={nodes.find((n) => n.id === logsNode)}
        />
      )}
    </>
  );
}

function MetaLine({
  run,
  done,
  total,
}: {
  run: Run;
  done: number;
  total: number;
}) {
  const pieces: string[] = [];
  if (run.git_branch)
    pieces.push(`${run.git_branch}@${(run.git_sha || "").slice(0, 10)}`);
  if (run.trigger_source) pieces.push(`trigger=${run.trigger_source}`);
  pieces.push(`status=${run.status}`);
  if (run.status === "running") pieces.push(`${done}/${total} nodes done`);
  else pieces.push(`duration=${formatDuration(runDurationMs(run))}`);
  return (
    <span className="text-xs text-[var(--muted)]">{pieces.join("  ·  ")}</span>
  );
}

// --- DAG ---

// computeLayers mirrors the vanilla dashboard's layering: each node's
// layer = max(upstream layer) + 1. Returns columns of node IDs.
function computeLayers(nodes: { id: string; deps: string[] }[]): string[][] {
  const byID: Record<string, { id: string; deps: string[] }> = {};
  for (const n of nodes) byID[n.id] = n;
  const depth: Record<string, number> = {};
  function resolve(id: string, stack = new Set<string>()): number {
    if (depth[id] !== undefined) return depth[id];
    if (stack.has(id)) return 0;
    stack.add(id);
    const n = byID[id];
    if (!n || !n.deps || n.deps.length === 0) {
      depth[id] = 0;
      return 0;
    }
    let d = 0;
    for (const dep of n.deps) d = Math.max(d, resolve(dep, stack) + 1);
    depth[id] = d;
    stack.delete(id);
    return d;
  }
  for (const n of nodes) resolve(n.id);
  const layers: string[][] = [];
  for (const n of nodes) {
    while (layers.length <= depth[n.id]) layers.push([]);
    layers[depth[n.id]].push(n.id);
  }
  return layers;
}

function Dag({
  layers,
  nodes,
  onClick,
}: {
  layers: string[][];
  nodes: import("@/lib/api").Node[];
  onClick: (id: string) => void;
}) {
  const byID = useMemo(() => {
    const m: Record<string, import("@/lib/api").Node> = {};
    for (const n of nodes) m[n.id] = n;
    return m;
  }, [nodes]);
  return (
    <div className="flex gap-6 items-start">
      {layers.map((layer, i) => (
        <div key={i} className="flex flex-col gap-3 min-w-[200px]">
          {layer.map((id) => {
            const n = byID[id];
            if (!n) return null;
            return <NodeCard key={id} node={n} onClick={() => onClick(id)} />;
          })}
        </div>
      ))}
    </div>
  );
}

function NodeCard({
  node,
  onClick,
}: {
  node: import("@/lib/api").Node;
  onClick: () => void;
}) {
  const flavor = node.outcome || node.status;
  const border = outcomeBorder(flavor);
  const holder = parseHolder(node.claimed_by);
  const terminal = node.status === "done";
  const lease = terminal ? "" : leaseLeft(node.lease_expires_at);

  return (
    <button
      onClick={onClick}
      className={`w-full bg-[var(--background)] border border-[var(--border)] border-l-[3px] ${border} rounded px-3 py-2 text-left hover:border-indigo-400 transition-colors`}
    >
      <div className="flex items-center gap-2">
        <span className="font-mono text-sm font-semibold truncate flex-1">
          {node.id}
        </span>
        <StatusPill status={flavor} />
      </div>
      <div className="mt-1 flex items-center justify-between text-[11px] text-[var(--muted)]">
        <span>{nodeDuration(node)}</span>
        <span>{node.deps.length ? `← ${node.deps.length}` : "root"}</span>
      </div>
      {(node.claimed_by || lease) && (
        <div className="mt-1 flex items-center justify-between text-[11px] font-mono">
          <span className={holderTextColor(holder.kind)}>
            on {holder.label}
          </span>
          {lease && <span className="text-[var(--muted)]">{lease}</span>}
        </div>
      )}
      {node.error && node.error !== "upstream-failed" && (
        <div className="mt-1 text-[11px] text-red-400 font-mono truncate">
          {node.error}
        </div>
      )}
    </button>
  );
}

function outcomeBorder(outcome: string): string {
  switch (outcome) {
    case "success":
      return "border-l-green-400";
    case "failed":
      return "border-l-red-400";
    case "running":
      return "border-l-indigo-400";
    case "cancelled":
      return "border-l-amber-400";
    case "cached":
      return "border-l-violet-400";
    case "satisfied":
      return "border-l-cyan-400";
    case "skipped":
      return "border-l-slate-500 opacity-70";
    case "skipped-concurrent":
      return "border-l-slate-600 opacity-70";
    case "superseded":
      return "border-l-amber-500";
    default:
      return "border-l-slate-500";
  }
}

function holderTextColor(kind: string): string {
  switch (kind) {
    case "pool":
      return "text-indigo-300";
    case "jobs":
      return "text-violet-300";
    case "cluster":
      return "text-cyan-300";
    default:
      return "text-[var(--muted)]";
  }
}

function nodeDuration(n: import("@/lib/api").Node): string {
  if (n.started_at && n.finished_at) return formatDuration(n.duration_ms);
  if (n.started_at) {
    const elapsed = Date.now() - new Date(n.started_at).getTime();
    return `running ${formatDuration(elapsed)}`;
  }
  if (n.status === "done") return "—";
  return "pending";
}

function leaseLeft(iso?: string): string {
  if (!iso) return "";
  const ms = new Date(iso).getTime() - Date.now();
  if (ms <= 0) return "lease expired";
  return `lease ${Math.ceil(ms / 1000)}s left`;
}

// --- Log drawer ---

function LogDrawer({
  runID,
  nodeID,
  node,
  onClose,
}: {
  runID: string;
  nodeID: string;
  node?: import("@/lib/api").Node;
  onClose: () => void;
}) {
  const [lines, setLines] = useState<string[]>([]);
  const [streaming, setStreaming] = useState(false);
  const preRef = useRef<HTMLPreElement>(null);

  // Initial GET for back-content, then SSE for live tail.
  useEffect(() => {
    let cancelled = false;
    let es: EventSource | null = null;

    const loadInitial = async () => {
      try {
        const text = await getNodeLogs(runID, nodeID);
        if (cancelled) return;
        if (text) setLines(text.split("\n"));
      } catch {
        // ignore
      }
    };

    const startStream = () => {
      if (cancelled) return;
      const url = getNodeStreamUrl(runID, nodeID);
      es = new EventSource(url);
      es.onopen = () => setStreaming(true);
      es.onmessage = (e) => {
        setLines((prev) => [...prev, e.data]);
      };
      es.onerror = () => {
        setStreaming(false);
        if (es && es.readyState === EventSource.CLOSED) {
          // fall back to GET once; run might have terminated
          loadInitial();
        }
      };
    };

    loadInitial().then(startStream);
    return () => {
      cancelled = true;
      if (es) es.close();
    };
  }, [runID, nodeID]);

  // Autoscroll to bottom on new line if already at bottom.
  useEffect(() => {
    const pre = preRef.current;
    if (!pre) return;
    const atBottom = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 40;
    if (atBottom) pre.scrollTop = pre.scrollHeight;
  }, [lines]);

  return (
    <div className="border-t border-[var(--border)] flex flex-col max-h-[45vh]">
      <div className="px-4 py-2 border-b border-[var(--border)] flex items-center gap-2">
        <h3 className="text-xs font-semibold uppercase text-[var(--muted)]">
          logs:{" "}
          <span className="font-mono text-[var(--foreground)]">{nodeID}</span>
        </h3>
        {node && (
          <span className="text-xs text-[var(--muted)]">
            {node.outcome || node.status}
          </span>
        )}
        <span
          className={`text-[10px] font-semibold uppercase ${streaming ? "text-green-400" : "text-[var(--muted)]"}`}
        >
          {streaming ? "live" : "paused"}
        </span>
        <div className="flex-1" />
        <button
          onClick={onClose}
          className="text-xs text-[var(--muted)] hover:text-[var(--foreground)] border border-[var(--border)] rounded px-2 py-0.5"
        >
          close
        </button>
      </div>
      <pre
        ref={preRef}
        className="flex-1 overflow-auto m-0 p-4 bg-[#0d1117] text-[#e6edf3] text-xs font-mono whitespace-pre-wrap"
      >
        {lines.length === 0 ? (
          <span className="text-[var(--muted)] italic">(no log output)</span>
        ) : (
          <span
            dangerouslySetInnerHTML={{ __html: ansiToHtml(lines.join("\n")) }}
          />
        )}
      </pre>
      <div className="border-t border-[var(--border)] p-3">
        <ResourceChart
          runID={runID}
          nodeID={nodeID}
          isRunning={node?.status !== "done"}
        />
      </div>
    </div>
  );
}
