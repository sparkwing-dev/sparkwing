"use client";

// Pipelines explorer: 3-column workflow view. Lists runs (left), the
// nodes inside the selected run (middle), and detail + logs for the
// selected node (right). Filters over repo / pipeline / branch /
// status / tag narrow the run list. Repo comes from the Run record
// populated at trigger / CreateRun; pipelines.yaml provides the tag
// set.
//
// Port of web/src/_stash/pipelines-page.tsx.bak, adapted to the Run /
// Node model. Fields the old Job carried but the new Run does not
// (heartbeat, status_detail, failure_reason, retry_of, prefer /
// require, env.TEST_NAME) are omitted rather than stubbed -- future
// plumbing sessions can re-introduce them when the backend stores
// them.

import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import { useSearchParams } from "next/navigation";
import {
  type Node as RunNode,
  type PipelineMeta,
  type Run,
  type RunDetail,
  cancelRun,
  getNodeLogs,
  getNodeStreamUrl,
  getPipelines,
  getRun,
  getRuns,
  parseHolder,
  retryRun,
  runDurationMs,
} from "@/lib/api";
import { useRunEvents } from "@/lib/useRunEvents";
import TriggerForm from "@/components/TriggerForm";
import StatusLabel from "@/components/StatusLabel";
import DebugPausePanel from "@/components/DebugPausePanel";
import Tooltip from "@/components/Tooltip";
import ExecutionWaterfall from "@/components/ExecutionWaterfall";
import ResourceChart from "@/components/ResourceChart";
import LogBucketView from "@/components/LogBucketView";
import SetupPanel from "@/components/SetupPanel";
import SummaryPanel from "@/components/SummaryPanel";
import SelectedNodePanel from "@/components/SelectedNodePanel";
import { parseLogLines } from "@/lib/logParser";
import ApprovalPane from "@/components/ApprovalPane";
import NodeWorkView from "@/components/NodeWorkView";

// Runs-list still polls: the event stream is per-run, not global, so
// the left sidebar can't subscribe to "anything new". The detail
// view (middle + right columns) is event-driven — see the
// useRunEvents wiring in Pipelines() below.
const POLL_MS = 2000;
// Fallback detail refresh when the event stream is unavailable
// (auth drop, proxy cuts the connection, browser tab backgrounded
// long enough for the server to timeout). Slower than pre-SSE since
// it's belt-and-suspenders, not the primary signal.
const DETAIL_FALLBACK_POLL_MS = 8000;
const RUNS_WINDOW = 200;

function fmtMs(ms: number): string {
  if (!ms) return "-";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms - m * 60_000) / 1000);
  return `${m}m ${s}s`;
}

function repoLabel(r: Run): string {
  const raw = r.repo || r.github_repo || "unknown";
  const slash = raw.lastIndexOf("/");
  return slash >= 0 ? raw.slice(slash + 1) : raw;
}

function statusDot(status: string): string {
  switch (status) {
    case "success":
    case "complete":
      return "bg-green-400";
    case "failed":
      return "bg-red-400";
    case "running":
    case "claimed":
      return "bg-indigo-400 animate-pulse";
    case "pending":
      return "bg-yellow-400 animate-pulse";
    case "paused":
      return "bg-yellow-400 animate-pulse ring-2 ring-yellow-400/60";
    case "cancelled":
      return "bg-gray-400";
    default:
      return "bg-gray-500";
  }
}

function outcomeDot(outcome: string, status: string): string {
  if (outcome) return statusDot(outcome === "success" ? "success" : outcome);
  return statusDot(status);
}

function TimeAgo({ ts }: { ts: string }) {
  const [, setTick] = useState(0);
  useEffect(() => {
    const i = setInterval(() => setTick((t) => t + 1), 1000);
    return () => clearInterval(i);
  }, []);
  const sec = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
  if (sec < 60) return <span>-{sec}s</span>;
  if (sec < 3600) return <span>-{Math.floor(sec / 60)}m</span>;
  if (sec < 86_400) return <span>-{Math.floor(sec / 3600)}h</span>;
  return <span>-{Math.floor(sec / 86_400)}d</span>;
}

function nodeDuration(n: RunNode): number {
  if (n.duration_ms) return n.duration_ms;
  if (n.started_at) {
    const s = new Date(n.started_at).getTime();
    const e = n.finished_at ? new Date(n.finished_at).getTime() : Date.now();
    return Math.max(0, e - s);
  }
  return 0;
}

export default function PipelinesPage() {
  return (
    <Suspense>
      <Pipelines />
    </Suspense>
  );
}

function Pipelines() {
  const searchParams = useSearchParams();
  const [runs, setRuns] = useState<Run[]>([]);
  const [pipelineMeta, setPipelineMeta] = useState<
    Record<string, PipelineMeta>
  >({});
  const [detail, setDetail] = useState<RunDetail | null>(null);
  const [selectedRun, setSelectedRun] = useState<string | null>(
    searchParams.get("run"),
  );
  const [selectedNode, setSelectedNode] = useState<string | null>(null);
  const [filterRepo, setFilterRepo] = useState<string[]>([]);
  const [filterPipeline, setFilterPipeline] = useState<string[]>([]);
  const [filterBranch, setFilterBranch] = useState<string[]>([]);
  const [filterStatus, setFilterStatus] = useState<string[]>([]);
  const [filterTag, setFilterTag] = useState<string[]>([]);
  const [openDropdown, setOpenDropdown] = useState<string | null>(null);
  const [showTrigger, setShowTrigger] = useState(false);
  const filterRef = useRef<HTMLDivElement>(null);

  const toggleFilter = (
    arr: string[],
    set: (v: string[]) => void,
    val: string,
  ) => {
    set(arr.includes(val) ? arr.filter((v) => v !== val) : [...arr, val]);
  };

  useEffect(() => {
    if (!openDropdown) return;
    const handler = (e: MouseEvent) => {
      if (filterRef.current && !filterRef.current.contains(e.target as Node)) {
        setOpenDropdown(null);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [openDropdown]);

  const refresh = useCallback(async () => {
    const [runList, meta] = await Promise.all([
      getRuns({ limit: RUNS_WINDOW }),
      getPipelines(),
    ]);
    setRuns(runList);
    setPipelineMeta(meta);
  }, []);

  const loadDetail = useCallback(async (runID: string) => {
    const d = await getRun(runID);
    if (d) setDetail(d);
  }, []);

  useEffect(() => {
    refresh();
    const i = setInterval(() => {
      if (!document.hidden) refresh();
    }, POLL_MS);
    return () => clearInterval(i);
  }, [refresh]);

  // Kick an initial detail fetch when a run is selected so the UI
  // has a baseline to mutate against. Subsequent updates come from
  // the SSE event stream (see the useRunEvents block just below) —
  // event-driven, ~sub-100ms latency, no 2s poll. A slow fallback
  // poll still fires while a run is selected in case the stream
  // can't open (auth drop, proxy cut, etc.).
  useEffect(() => {
    if (!selectedRun) {
      setDetail(null);
      return;
    }
    loadDetail(selectedRun);
    const i = setInterval(() => {
      if (!document.hidden) loadDetail(selectedRun);
    }, DETAIL_FALLBACK_POLL_MS);
    return () => clearInterval(i);
  }, [selectedRun, loadDetail]);

  // Coalesced refetch driver: every event from the stream marks the
  // detail as stale. A single in-flight fetch drains the staleness;
  // if more events arrive mid-fetch, a trailing fetch fires. This
  // keeps us O(1) network calls per burst of events rather than one
  // per event.
  const refetchState = useRef<{ inFlight: boolean; stale: boolean }>({
    inFlight: false,
    stale: false,
  });
  const kickRefetch = useCallback(() => {
    if (!selectedRun) return;
    if (refetchState.current.inFlight) {
      refetchState.current.stale = true;
      return;
    }
    const run = async () => {
      refetchState.current.inFlight = true;
      try {
        await loadDetail(selectedRun);
      } finally {
        refetchState.current.inFlight = false;
        if (refetchState.current.stale) {
          refetchState.current.stale = false;
          run();
        }
      }
    };
    run();
  }, [selectedRun, loadDetail]);

  useRunEvents(selectedRun, {
    onEvent: () => {
      if (document.hidden) return;
      kickRefetch();
    },
  });

  const repos = [...new Set(runs.map(repoLabel))].sort();
  const pipelines = [...new Set(runs.map((r) => r.pipeline))].sort();
  const branches = [
    ...new Set(runs.map((r) => r.git_branch || "").filter(Boolean)),
  ].sort();
  const allTags = [
    ...new Set(Object.values(pipelineMeta).flatMap((m) => m.tags || [])),
  ].sort();
  const statuses = ["success", "failed", "running", "cancelled"];

  const topLevel = runs.filter((r) => {
    if (filterRepo.length && !filterRepo.includes(repoLabel(r))) return false;
    if (filterPipeline.length && !filterPipeline.includes(r.pipeline))
      return false;
    if (filterBranch.length && !filterBranch.includes(r.git_branch || ""))
      return false;
    if (filterStatus.length && !filterStatus.includes(r.status)) return false;
    if (filterTag.length) {
      const tags = pipelineMeta[r.pipeline]?.tags || [];
      if (!filterTag.some((t) => tags.includes(t))) return false;
    }
    return true;
  });

  const activeFilterCount =
    filterRepo.length +
    filterPipeline.length +
    filterBranch.length +
    filterStatus.length +
    filterTag.length;

  const run = detail?.run || null;
  const nodes = detail?.nodes || [];
  const node = nodes.find((n) => n.id === selectedNode) || null;

  const selectRun = (id: string | null) => {
    setSelectedRun(id);
    setSelectedNode(null);
    if (id) window.history.replaceState(null, "", `?run=${id}`);
    else window.history.replaceState(null, "", "/runs");
  };

  return (
    <div className="flex flex-1 overflow-hidden">
      {/* Left: Runs list */}
      <div
        className={`${run ? "w-52" : "w-[28rem]"} border-r border-[var(--border)] flex flex-col shrink-0 transition-all`}
      >
        {run && (
          <div className="px-3 py-2 border-b border-[var(--border)] text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
            Runs
          </div>
        )}

        <div ref={filterRef} className="border-b border-[var(--border)]">
          {run ? (
            <CompactFilterBar
              openDropdown={openDropdown}
              setOpenDropdown={setOpenDropdown}
              activeFilterCount={activeFilterCount}
              clear={() => {
                setFilterRepo([]);
                setFilterPipeline([]);
                setFilterBranch([]);
                setFilterStatus([]);
                setFilterTag([]);
              }}
              groups={[
                {
                  key: "repo",
                  label: "Repo",
                  values: filterRepo,
                  set: setFilterRepo,
                  options: repos,
                  activeText: "text-cyan-300",
                  activeBg: "bg-cyan-500/15",
                },
                {
                  key: "pipeline",
                  label: "Pipeline",
                  values: filterPipeline,
                  set: setFilterPipeline,
                  options: pipelines,
                  activeText: "text-violet-300",
                  activeBg: "bg-violet-500/15",
                },
                {
                  key: "tag",
                  label: "Tag",
                  values: filterTag,
                  set: setFilterTag,
                  options: allTags,
                  activeText: "text-pink-300",
                  activeBg: "bg-pink-500/15",
                },
                {
                  key: "branch",
                  label: "Branch",
                  values: filterBranch,
                  set: setFilterBranch,
                  options: branches,
                  activeText: "text-amber-300",
                  activeBg: "bg-amber-500/15",
                },
                {
                  key: "status",
                  label: "Status",
                  values: filterStatus,
                  set: setFilterStatus,
                  options: statuses,
                  activeText: "text-emerald-300",
                  activeBg: "bg-emerald-500/15",
                },
              ]}
              toggleFilter={toggleFilter}
            />
          ) : (
            <FullFilterBar
              openDropdown={openDropdown}
              setOpenDropdown={setOpenDropdown}
              activeFilterCount={activeFilterCount}
              clearAll={() => {
                setFilterRepo([]);
                setFilterPipeline([]);
                setFilterBranch([]);
                setFilterStatus([]);
                setFilterTag([]);
              }}
              groups={[
                {
                  key: "repo",
                  label: "REPO",
                  values: filterRepo,
                  set: setFilterRepo,
                  options: repos,
                  color: "text-cyan-400",
                  activeBg: "bg-cyan-500/15",
                  activeText: "text-cyan-300",
                },
                {
                  key: "pipeline",
                  label: "PIPELINE",
                  values: filterPipeline,
                  set: setFilterPipeline,
                  options: pipelines,
                  color: "text-violet-400",
                  activeBg: "bg-violet-500/15",
                  activeText: "text-violet-300",
                },
                {
                  key: "tag",
                  label: "TAG",
                  values: filterTag,
                  set: setFilterTag,
                  options: allTags,
                  color: "text-pink-400",
                  activeBg: "bg-pink-500/15",
                  activeText: "text-pink-300",
                },
                {
                  key: "branch",
                  label: "BRANCH",
                  values: filterBranch,
                  set: setFilterBranch,
                  options: branches,
                  color: "text-amber-400",
                  activeBg: "bg-amber-500/15",
                  activeText: "text-amber-300",
                },
                {
                  key: "status",
                  label: "STATUS",
                  values: filterStatus,
                  set: setFilterStatus,
                  options: statuses,
                  color: "text-emerald-400",
                  activeBg: "bg-emerald-500/15",
                  activeText: "text-emerald-300",
                },
              ]}
              toggleFilter={toggleFilter}
            />
          )}
        </div>

        <div className="flex-1 overflow-y-auto">
          {topLevel.map((r) => {
            const isActive = selectedRun === r.id;
            return (
              <div
                key={r.id}
                data-run-id={r.id}
                onClick={() => selectRun(isActive ? null : r.id)}
                className={`px-3 py-2 border-b border-[var(--border)] cursor-pointer hover:bg-[var(--surface-raised)] transition-colors ${isActive ? "bg-[var(--surface-raised)] border-l-2 border-l-[var(--accent)]" : ""}`}
              >
                {run ? <CompactRunRow r={r} /> : <FullRunRow r={r} />}
              </div>
            );
          })}
          {topLevel.length === 0 && (
            <div className="p-8 text-center text-[var(--muted)] text-sm">
              {activeFilterCount > 0 ? "No matching runs" : "No runs yet"}
            </div>
          )}
        </div>
      </div>

      {/* Middle: RunNodes in run */}
      {run && detail && (
        <div className="w-56 border-r border-[var(--border)] flex flex-col shrink-0 overflow-y-auto">
          <div className="px-3 py-2 border-b border-[var(--border)] text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
            Nodes ({nodes.length})
          </div>
          <NodesList
            nodes={nodes}
            selectedNode={selectedNode}
            onSelect={setSelectedNode}
          />
        </div>
      )}

      {/* Right: detail + logs */}
      <div className="flex-1 flex flex-col overflow-hidden">
        {!run ? (
          <div className="flex-1 flex items-center justify-center text-[var(--muted)] text-sm">
            ← Select a run to view its nodes and logs
          </div>
        ) : (
          <RunDetailPane
            run={run}
            nodes={nodes}
            node={node}
            showTrigger={showTrigger}
            setShowTrigger={setShowTrigger}
            onSelectNode={setSelectedNode}
            onRefresh={() => {
              refresh();
              if (selectedRun) loadDetail(selectedRun);
            }}
          />
        )}
      </div>
    </div>
  );
}

// --- nodes list (middle column) ---

// partitionByGroup walks the nodes in order and produces a list of
// sections preserving the plan's original sequencing. A group is
// "opened" at the position of its first member; subsequent members
// collect into that same section even if other nodes appear between
// them, so the header stays anchored to where the group begins in
// the plan. Ungrouped nodes stay inline and get split into runs
// around any grouped section they straddle -- this is what makes the
// group header show up where the author put it in the DSL, not
// bottom-pinned.
function partitionByGroup(
  nodes: RunNode[],
): { group: string; nodes: RunNode[] }[] {
  const sections: { group: string; nodes: RunNode[] }[] = [];
  const groupSection = new Map<string, number>();
  for (const n of nodes) {
    // A node may belong to multiple named groups; for partitioning we
    // anchor it to its first declared group (the primary cluster).
    const g = n.groups?.[0] || "";
    if (g === "") {
      const last = sections[sections.length - 1];
      if (last && last.group === "") {
        last.nodes.push(n);
      } else {
        sections.push({ group: "", nodes: [n] });
      }
      continue;
    }
    let idx = groupSection.get(g);
    if (idx === undefined) {
      idx = sections.length;
      sections.push({ group: g, nodes: [] });
      groupSection.set(g, idx);
    }
    sections[idx].nodes.push(n);
  }
  return sections;
}

type GroupAgg = "failed" | "running" | "pending" | "success";

// aggregateGroupStatus reduces a group's child nodes into a single
// pill status. Priority failed > running > pending > success matches
// the design doc: the most-attention-worthy state wins. "claimed" is
// treated as running; cached/skipped count toward success.
function aggregateGroupStatus(nodes: RunNode[]): GroupAgg {
  let hasRunning = false;
  let hasPending = false;
  for (const n of nodes) {
    const k = n.outcome || n.status;
    if (k === "failed") return "failed";
    if (k === "running" || k === "claimed") hasRunning = true;
    else if (k === "pending") hasPending = true;
  }
  if (hasRunning) return "running";
  if (hasPending) return "pending";
  return "success";
}

function NodesList({
  nodes,
  selectedNode,
  onSelect,
}: {
  nodes: RunNode[];
  selectedNode: string | null;
  onSelect: (id: string) => void;
}) {
  const groups = partitionByGroup(nodes);
  // Collapse state is keyed on the group name and driven by the
  // design-doc default: expanded while anything's still moving or
  // failed; collapsed once every child succeeded. The user can
  // override either way by clicking the header; we track that as an
  // explicit toggle so auto-collapse doesn't fight them.
  const [overrides, setOverrides] = useState<Record<string, boolean>>({});
  const toggle = (g: string) =>
    setOverrides((prev) => {
      const agg = aggregateGroupStatus(
        groups.find((x) => x.group === g)?.nodes || [],
      );
      const defaultCollapsed = agg === "success";
      const current = prev[g] ?? defaultCollapsed;
      return { ...prev, [g]: !current };
    });

  return (
    <>
      {groups.map(({ group, nodes: children }) => {
        if (!group) {
          // Ungrouped nodes render flat at the top — preserves the
          // pre-group look for pipelines that haven't opted in.
          return children.map((n) => (
            <NodeRow
              key={n.id}
              n={n}
              selected={selectedNode === n.id}
              onSelect={onSelect}
            />
          ));
        }
        const agg = aggregateGroupStatus(children);
        const defaultCollapsed = agg === "success";
        const collapsed = overrides[group] ?? defaultCollapsed;
        return (
          <div key={group}>
            <GroupHeader
              name={group}
              agg={agg}
              count={children.length}
              collapsed={collapsed}
              onToggle={() => toggle(group)}
            />
            {!collapsed &&
              children.map((n) => (
                <NodeRow
                  key={n.id}
                  n={n}
                  selected={selectedNode === n.id}
                  indent
                  onSelect={onSelect}
                />
              ))}
          </div>
        );
      })}
    </>
  );
}

function GroupHeader({
  name,
  agg,
  count,
  collapsed,
  onToggle,
}: {
  name: string;
  agg: GroupAgg;
  count: number;
  collapsed: boolean;
  onToggle: () => void;
}) {
  return (
    <button
      onClick={onToggle}
      className="w-full flex items-center gap-2 px-3 py-2 border-b border-[var(--border)] text-left hover:bg-[var(--surface-raised)] transition-colors"
    >
      <span className="w-3 text-center text-[var(--muted)] text-[10px]">
        {collapsed ? "▸" : "▾"}
      </span>
      <Tooltip content={`${agg} (${count} node${count === 1 ? "" : "s"})`}>
        <span className={`w-2 h-2 rounded-full shrink-0 ${statusDot(agg)}`} />
      </Tooltip>
      <span className="text-xs text-[var(--muted)] truncate">({name})</span>
      <span className="ml-auto text-[10px] font-mono text-[var(--muted)] shrink-0">
        {count}
      </span>
    </button>
  );
}

function NodeRow({
  n,
  selected,
  indent,
  onSelect,
}: {
  n: RunNode;
  selected: boolean;
  indent?: boolean;
  onSelect: (id: string) => void;
}) {
  return (
    <div
      className={`${indent ? "pl-6 pr-3" : "px-3"} py-2 border-b border-[var(--border)] cursor-pointer hover:bg-[var(--surface-raised)] transition-colors ${selected ? "bg-[var(--surface-raised)] border-l-2 border-l-indigo-400" : ""}`}
      onClick={() => onSelect(n.id)}
    >
      <div className="flex items-center gap-2">
        <Tooltip
          content={
            <>
              {n.outcome || n.status}
              {nodeDuration(n) ? ` in ${fmtMs(nodeDuration(n))}` : ""}
            </>
          }
        >
          <span
            className={`w-2 h-2 rounded-full shrink-0 ${outcomeDot(n.outcome, n.status)}`}
          />
        </Tooltip>
        <span className="text-xs truncate">{n.id}</span>
        <span className="ml-auto text-[10px] font-mono text-[var(--muted)] shrink-0">
          {fmtMs(nodeDuration(n))}
        </span>
      </div>
    </div>
  );
}

// --- run row variants ---

function FullRunRow({ r }: { r: Run }) {
  return (
    <>
      <div className="flex items-center gap-2 mb-0.5">
        <StatusLabel status={r.status} />
        <span className="text-cyan-400/70 text-xs">{repoLabel(r)}</span>
        <span className="text-[var(--muted)] text-xs">/</span>
        <span className="font-medium text-sm text-violet-300 truncate">
          {r.pipeline}
        </span>
        <span className="ml-auto text-xs font-mono shrink-0">
          {fmtMs(runDurationMs(r))}
        </span>
      </div>
      <div className="flex items-center gap-2 text-xs text-[var(--muted)]">
        {r.git_branch && (
          <span className="text-amber-400/70">⎇ {r.git_branch}</span>
        )}
        {r.git_sha && (
          <span className="font-mono">{r.git_sha.slice(0, 7)}</span>
        )}
        <span className="ml-auto font-mono">
          {new Date(r.started_at).toLocaleTimeString()}
        </span>
        <span>
          (<TimeAgo ts={r.started_at} />)
        </span>
      </div>
    </>
  );
}

function CompactRunRow({ r }: { r: Run }) {
  return (
    <div className="flex items-center gap-2">
      <Tooltip
        content={
          <>
            {r.status}
            {(() => {
              const ms = runDurationMs(r);
              return ms ? ` in ${fmtMs(ms)}` : "";
            })()}
          </>
        }
      >
        <span
          className={`w-2.5 h-2.5 rounded-full shrink-0 ${statusDot(r.status)}`}
        />
      </Tooltip>
      <Tooltip
        content={
          <>
            <span className="text-[var(--muted)]">Pipeline:</span> {r.pipeline}
            <br />
            <span className="text-[var(--muted)]">Repo:</span> {repoLabel(r)}
            <br />
            <span className="text-[var(--muted)]">Branch:</span>{" "}
            {r.git_branch || "-"}
            <br />
            <span className="text-[var(--muted)]">ID:</span>{" "}
            <span className="font-mono">{r.id}</span>
          </>
        }
      >
        <span className="text-xs text-violet-300 truncate">{r.pipeline}</span>
      </Tooltip>
      <span className="ml-auto shrink-0 text-[10px] font-mono text-[var(--muted)]">
        <TimeAgo ts={r.started_at} />
      </span>
    </div>
  );
}

// --- filter bars ---

interface FilterGroup {
  key: string;
  label: string;
  values: string[];
  set: (v: string[]) => void;
  options: string[];
  color?: string;
  activeBg: string;
  activeText: string;
}

function FullFilterBar({
  openDropdown,
  setOpenDropdown,
  activeFilterCount,
  clearAll,
  groups,
  toggleFilter,
}: {
  openDropdown: string | null;
  setOpenDropdown: (v: string | null) => void;
  activeFilterCount: number;
  clearAll: () => void;
  groups: FilterGroup[];
  toggleFilter: (
    arr: string[],
    set: (v: string[]) => void,
    val: string,
  ) => void;
}) {
  return (
    <>
      <div className="flex items-center px-2 py-1.5 gap-1 flex-wrap">
        <span className="text-[var(--muted)] text-xs mr-0.5">Filter:</span>
        {groups.map((f) => (
          <div key={f.key} className="relative">
            <button
              onClick={() =>
                setOpenDropdown(openDropdown === f.key ? null : f.key)
              }
              className={`px-2 py-0.5 rounded text-[10px] font-bold tracking-wider transition-colors ${
                f.values.length
                  ? `${f.activeBg} ${f.activeText}`
                  : `text-[var(--muted)] hover:${f.color || ""}`
              }`}
            >
              {f.label}
              {f.values.length > 0 ? ` (${f.values.length})` : ""}{" "}
              <span className="text-[8px]">▾</span>
            </button>
            {openDropdown === f.key && (
              <div className="absolute top-full left-0 mt-1 bg-[var(--surface)] border border-[var(--border)] rounded-lg shadow-lg z-50 min-w-[160px] max-h-64 overflow-y-auto">
                {f.values.length > 0 && (
                  <button
                    onClick={() => f.set([])}
                    className="w-full text-left px-3 py-1.5 text-xs hover:bg-[var(--surface-raised)] text-[var(--muted)] border-b border-[var(--border)]"
                  >
                    Clear all
                  </button>
                )}
                {f.options.map((opt) => {
                  const isSelected = f.values.includes(opt);
                  return (
                    <button
                      key={opt}
                      onClick={() => toggleFilter(f.values, f.set, opt)}
                      className={`w-full text-left px-3 py-1.5 text-xs hover:bg-[var(--surface-raised)] font-mono flex items-center gap-2 ${isSelected ? f.activeText : ""}`}
                    >
                      <span
                        className={`w-3.5 h-3.5 rounded border flex items-center justify-center text-[10px] ${isSelected ? `${f.activeBg} border-current` : "border-[var(--border)]"}`}
                      >
                        {isSelected && "✓"}
                      </span>
                      {f.key === "branch" ? `⎇ ${opt}` : opt}
                    </button>
                  );
                })}
                {f.options.length === 0 && (
                  <div className="px-3 py-2 text-[var(--muted)] text-xs">
                    no options yet
                  </div>
                )}
              </div>
            )}
          </div>
        ))}
      </div>
      {activeFilterCount > 0 && (
        <div className="flex items-center gap-1 px-2 pb-1.5 flex-wrap">
          {groups.flatMap((f) =>
            f.values.map((v) => (
              <span
                key={`${f.key}-${v}`}
                className={`inline-flex items-center gap-1 ${f.activeBg} ${f.activeText} px-2 py-0.5 rounded text-xs font-mono`}
              >
                {f.key === "branch" ? `⎇ ${v}` : v}
                <button
                  onClick={() => toggleFilter(f.values, f.set, v)}
                  className="hover:text-white"
                >
                  ×
                </button>
              </span>
            )),
          )}
          <button
            onClick={clearAll}
            className="text-[10px] text-[var(--muted)] hover:text-[var(--foreground)] ml-1"
          >
            clear all
          </button>
        </div>
      )}
    </>
  );
}

function CompactFilterBar({
  openDropdown,
  setOpenDropdown,
  activeFilterCount,
  clear,
  groups,
  toggleFilter,
}: {
  openDropdown: string | null;
  setOpenDropdown: (v: string | null) => void;
  activeFilterCount: number;
  clear: () => void;
  groups: FilterGroup[];
  toggleFilter: (
    arr: string[],
    set: (v: string[]) => void,
    val: string,
  ) => void;
}) {
  return (
    <div className="relative flex items-center px-2 py-1.5 gap-1">
      <button
        onClick={() =>
          setOpenDropdown(
            openDropdown === "compact-filter" ? null : "compact-filter",
          )
        }
        className={`text-[10px] transition-colors ${activeFilterCount > 0 ? "text-[var(--foreground)]" : "text-[var(--muted)] hover:text-[var(--foreground)]"}`}
      >
        Filter{activeFilterCount > 0 ? ` (${activeFilterCount})` : ""}{" "}
        <span className="text-[8px]">▾</span>
      </button>
      {activeFilterCount > 0 && (
        <button
          onClick={clear}
          className="text-[10px] text-[var(--muted)] hover:text-[var(--foreground)] ml-auto"
        >
          clear
        </button>
      )}
      {openDropdown === "compact-filter" && (
        <div className="absolute top-full left-0 mt-1 bg-[var(--surface)] border border-[var(--border)] rounded-lg shadow-lg z-50 min-w-[220px] p-2 space-y-2 max-h-[70vh] overflow-y-auto">
          {groups.map((f) => (
            <div key={f.key}>
              <div className="text-[10px] text-[var(--muted)] font-bold uppercase tracking-wider mb-1">
                {f.label}
              </div>
              {f.options.map((opt) => {
                const isChecked = f.values.includes(opt);
                return (
                  <button
                    key={opt}
                    onClick={() => toggleFilter(f.values, f.set, opt)}
                    className={`w-full text-left px-2 py-1 text-xs hover:bg-[var(--surface-raised)] font-mono flex items-center gap-2 rounded ${isChecked ? f.activeText : ""}`}
                  >
                    <span
                      className={`w-3 h-3 rounded border flex items-center justify-center text-[9px] ${isChecked ? `${f.activeBg} border-current` : "border-[var(--border)]"}`}
                    >
                      {isChecked && "✓"}
                    </span>
                    {f.key === "branch" ? `⎇ ${opt}` : opt}
                  </button>
                );
              })}
              {f.options.length === 0 && (
                <div className="px-2 py-1 text-[var(--muted)] text-[10px]">
                  no options
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// --- detail pane ---

function RunDetailPane({
  run,
  nodes,
  node,
  showTrigger,
  setShowTrigger,
  onSelectNode,
  onRefresh,
}: {
  run: Run;
  nodes: RunNode[];
  node: RunNode | null;
  showTrigger: boolean;
  setShowTrigger: (v: boolean) => void;
  onSelectNode: (id: string) => void;
  onRefresh: () => void;
}) {
  const selected = node;
  const selectedIsRunning =
    !!selected && !selected.finished_at && selected.status !== "pending";
  const runIsActive = run.status === "running";
  const isTerminal =
    run.status === "success" ||
    run.status === "failed" ||
    run.status === "cancelled";
  const hasWork = !!(selected && (selected.work || selected.modifiers));

  type TabKey =
    | "logs"
    | "work"
    | "resources"
    | "dag"
    | "timeline"
    | "summary"
    | "setup";

  const tabs: {
    key: TabKey;
    label: string;
    count?: string;
    visible: boolean;
  }[] = [
    { key: "logs", label: "Logs", visible: !!selected },
    {
      key: "work",
      label: "Work",
      count: hasWork ? `${selected?.work?.steps?.length ?? 0}` : undefined,
      visible: hasWork,
    },
    { key: "resources", label: "Resources", visible: !!selected },
    {
      key: "dag",
      label: "DAG",
      count: nodes.length ? `${nodes.length}` : undefined,
      visible: nodes.length > 0,
    },
    { key: "timeline", label: "Timeline", visible: nodes.length > 0 },
    { key: "summary", label: "Summary", visible: isTerminal },
    { key: "setup", label: "Setup", visible: true },
  ];
  const visibleTabs = tabs.filter((t) => t.visible);

  const selectedId = selected?.id ?? null;
  const [tab, setTab] = useState<TabKey>(
    selected
      ? "logs"
      : isTerminal
        ? "summary"
        : nodes.length > 0
          ? "dag"
          : "setup",
  );
  const tabRef = useRef<TabKey>(tab);
  useEffect(() => {
    tabRef.current = tab;
  }, [tab]);
  const prevSelectedRef = useRef<string | null>(selectedId);

  // Selection-driven tab routing: clicking a node should pull the
  // detail pane to that node's logs, but only when there's a reason
  // to switch — either we had no selection before, or the user is on
  // a run-scoped tab where node-level info isn't visible. If they're
  // already on a node-scoped tab (logs/work/resources), preserve it
  // when bouncing between nodes.
  useEffect(() => {
    const prev = prevSelectedRef.current;
    prevSelectedRef.current = selectedId;
    if (!selectedId) return;
    const t = tabRef.current;
    if (
      !prev ||
      t === "dag" ||
      t === "timeline" ||
      t === "summary" ||
      t === "setup"
    ) {
      setTab("logs");
    }
  }, [selectedId]);

  const effectiveTab: TabKey =
    visibleTabs.find((t) => t.key === tab)?.key ??
    visibleTabs[0]?.key ??
    "logs";

  return (
    <>
      <div className="border-b border-[var(--border)] shrink-0">
        <div className="flex items-center gap-2 px-4 py-2 text-xs">
          <div className="flex items-center gap-2">
            <StatusLabel status={run.status} />
            <span className="text-cyan-400">{repoLabel(run)}</span>
            <span className="text-[var(--muted)]">/</span>
            <span className="font-bold text-sm text-violet-300">
              {run.pipeline}
            </span>
          </div>
          <span
            className="font-mono text-[var(--muted)] cursor-pointer hover:text-[var(--foreground)]"
            onClick={() => navigator.clipboard.writeText(run.id)}
            title="copy run id"
          >
            #{run.id}
          </span>
          <span className="ml-auto flex items-center gap-2">
            {runIsActive && <CancelButton runId={run.id} onDone={onRefresh} />}
            <RetryButton runId={run.id} onDone={onRefresh} />
            <button
              onClick={() => setShowTrigger(!showTrigger)}
              className="bg-green-500/20 text-green-400 border border-green-500/30 px-2 py-1 rounded text-xs font-medium hover:bg-green-500/30 transition-colors"
            >
              Run
            </button>
          </span>
        </div>

        <div className="px-4 pb-2">
          <DebugPausePanel runID={run.id} runStatus={run.status} />
        </div>

        <PendingApprovalsBanner
          runID={run.id}
          nodes={nodes}
          onSelectNode={onSelectNode}
        />
      </div>

      {selected && <SelectedNodePanel node={selected} />}

      {showTrigger && (
        <div className="border-b border-[var(--border)] shrink-0 p-4">
          <TriggerForm
            pipeline={run.pipeline}
            onTriggered={() => {
              setShowTrigger(false);
              onRefresh();
            }}
            onClose={() => setShowTrigger(false)}
          />
        </div>
      )}

      <div className="border-b border-[var(--border)] shrink-0 flex items-center gap-1 px-2 bg-[var(--surface)] overflow-x-auto">
        {visibleTabs.map((t) => (
          <button
            key={t.key}
            onClick={() => setTab(t.key)}
            className={`text-xs px-3 py-2 border-b-2 transition-colors -mb-px whitespace-nowrap ${
              effectiveTab === t.key
                ? "border-cyan-400 text-[var(--foreground)]"
                : "border-transparent text-[var(--muted)] hover:text-[var(--foreground)]"
            }`}
          >
            <span className="font-semibold">{t.label}</span>
            {t.count && (
              <span className="ml-1.5 font-mono text-[var(--muted)]">
                {t.count}
              </span>
            )}
          </button>
        ))}
      </div>

      <div className="flex-1 overflow-y-auto bg-[#0d1117] relative">
        {effectiveTab === "logs" && (
          <div className="p-4">
            <LogsPane run={run} node={selected} />
          </div>
        )}
        {effectiveTab === "work" && selected && (
          <div className="p-4">
            <NodeWorkView node={selected} />
          </div>
        )}
        {effectiveTab === "resources" && selected && (
          <div className="p-4">
            <ResourceChart
              runID={run.id}
              nodeID={selected.id}
              isRunning={selectedIsRunning}
            />
          </div>
        )}
        {effectiveTab === "dag" && (
          <div className="p-4">
            <DAG
              nodes={nodes}
              selected={selected?.id || null}
              onSelect={onSelectNode}
            />
          </div>
        )}
        {effectiveTab === "timeline" && (
          <div className="p-4">
            <ExecutionWaterfall run={run} nodes={nodes} />
          </div>
        )}
        {effectiveTab === "summary" && (
          <SummaryPanel
            run={run}
            nodes={nodes}
            collapsed={false}
            onToggle={() => {}}
            inline
          />
        )}
        {effectiveTab === "setup" && (
          <SetupPanel
            run={run}
            collapsed={false}
            onToggle={() => {}}
            inline
            onOpenRun={(id) => {
              const el = document.querySelector(`[data-run-id="${id}"]`);
              if (el) (el as HTMLElement).click();
              else window.location.assign(`?run=${id}`);
            }}
          />
        )}
      </div>
    </>
  );
}

// PendingApprovalsBanner surfaces every approval_pending node at the
// top of the detail pane so operators can approve / deny without
// having to click through to the specific node in the middle column.
// One ApprovalPane per pending gate; clicking the header jumps the
// log pane to that node so the usual context (step output, pause
// controls, etc.) is still one click away.
function PendingApprovalsBanner({
  runID,
  nodes,
  onSelectNode,
}: {
  runID: string;
  nodes: RunNode[];
  onSelectNode: (id: string) => void;
}) {
  const pending = nodes.filter((n) => n.status === "approval_pending");
  if (pending.length === 0) return null;
  return (
    <div className="px-4 pb-3 space-y-2">
      {pending.map((n) => (
        <div key={n.id}>
          <button
            onClick={() => onSelectNode(n.id)}
            className="w-full text-left text-[10px] font-bold uppercase tracking-wider text-yellow-300 hover:text-yellow-200 transition-colors mb-1 flex items-center gap-2"
          >
            <span className="w-2 h-2 rounded-full bg-yellow-400 animate-pulse" />
            Awaiting approval · {n.id}
          </button>
          <ApprovalPane runID={runID} nodeID={n.id} />
        </div>
      ))}
    </div>
  );
}

// --- logs ---

function LogsPane({ run, node }: { run: Run; node: RunNode | null }) {
  if (!node) {
    return (
      <div className="text-sm text-[var(--muted)]">
        ← Select a node to view its logs
      </div>
    );
  }
  if (node.status === "pending") {
    return (
      <div className="text-sm text-[var(--muted)]">
        Node is pending -- waiting for dependencies.
      </div>
    );
  }
  // Approval gate logs still stream here while waiting; the
  // approve/deny banner itself has moved up to RunDetailPane so
  // users can action the gate without clicking the specific node.
  if (node.status === "approval_pending") {
    return <StreamingLogs runID={run.id} nodeID={node.id} />;
  }
  const isLive = !node.finished_at;
  if (isLive) {
    return <StreamingLogs runID={run.id} nodeID={node.id} />;
  }
  return <StoredLogs runID={run.id} nodeID={node.id} />;
}

function StreamingLogs({ runID, nodeID }: { runID: string; nodeID: string }) {
  const [lines, setLines] = useState<string[]>([]);
  const endRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    setLines([]);
    const url = getNodeStreamUrl(runID, nodeID);
    const es = new EventSource(url, { withCredentials: true });
    es.onmessage = (e) => {
      // SSE may bundle several JSONL records into one data chunk
      // (one-per-line). Split so each record becomes its own entry
      // in state — parseLogLines wants line granularity to detect
      // JSONL vs legacy text.
      const incoming = (e.data as string).split("\n").filter((s) => s !== "");
      setLines((prev) => [...prev, ...incoming]);
    };
    es.onerror = () => {
      es.close();
    };
    return () => es.close();
  }, [runID, nodeID]);

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [lines.length]);

  if (lines.length === 0) {
    return <div className="text-sm text-[var(--muted)] p-4">streaming...</div>;
  }
  const parsed = parseLogLines(lines);
  return (
    <>
      <LogBucketView parsed={parsed} jobId={`${runID}-${nodeID}`} />
      <div ref={endRef} />
    </>
  );
}

function StoredLogs({ runID, nodeID }: { runID: string; nodeID: string }) {
  const [text, setText] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setText(null);
    (async () => {
      const t = await getNodeLogs(runID, nodeID);
      if (!cancelled) setText(t);
    })();
    return () => {
      cancelled = true;
    };
  }, [runID, nodeID]);

  if (text === null) {
    return <div className="text-sm text-[var(--muted)]">loading...</div>;
  }
  if (text.trim() === "") {
    return (
      <div className="text-sm text-[var(--muted)]">
        No logs captured for this node.
      </div>
    );
  }
  const parsed = parseLogLines(text.split("\n"));
  return <LogBucketView parsed={parsed} jobId={`${runID}-${nodeID}`} />;
}

// --- DAG ---

// DAG renders nodes laid out in columns by topological depth, with
// bezier edges drawn from each dep's right side to the node's left.
// Click a node to select it (same effect as clicking its row in the
// middle column). The layout is purely structural -- timing lives in
// the Timeline block below. Node width / gaps chosen to keep the
// whole graph visible on typical dashboards without scrolling; wide
// DAGs scroll horizontally.
function DAG({
  nodes,
  selected,
  onSelect,
}: {
  nodes: RunNode[];
  selected: string | null;
  onSelect: (id: string) => void;
}) {
  // Hover state for the floating tooltip overlay. Tracks which node
  // the pointer is currently over plus its viewport coords so we can
  // render a position:fixed card next to the cursor.
  const [hover, setHover] = useState<{
    node: RunNode;
    x: number;
    y: number;
  } | null>(null);
  const nodeW = 168;
  const nodeH = 38;
  const colGap = 64;
  const rowGap = 14;
  const padX = 12;
  const padY = 12;

  const byID = new Map(nodes.map((n) => [n.id, n]));
  // Treat `on_failure_of` as a virtual dep for column placement: a
  // rollback node anchored to its parent sits one column to the
  // right of the parent, so it doesn't strand at level 0 as an
  // island. Still rendered with a distinct dashed edge below.
  const effectiveDeps = (n: RunNode): string[] => {
    const base = n.deps || [];
    if (n.on_failure_of) return [...base, n.on_failure_of];
    return base;
  };
  const level = new Map<string, number>();
  const resolve = (id: string): number => {
    const cached = level.get(id);
    if (cached !== undefined) return cached;
    const n = byID.get(id);
    if (!n) {
      level.set(id, 0);
      return 0;
    }
    const deps = effectiveDeps(n);
    if (deps.length === 0) {
      level.set(id, 0);
      return 0;
    }
    // Guard against cycles: pre-seed self at 0 so a self-loop
    // collapses to the same level rather than recursing forever.
    level.set(id, 0);
    let l = 0;
    for (const d of deps) {
      if (byID.has(d)) l = Math.max(l, resolve(d) + 1);
    }
    level.set(id, l);
    return l;
  };
  for (const n of nodes) resolve(n.id);

  const columns: RunNode[][] = [];
  for (const n of nodes) {
    const l = level.get(n.id) ?? 0;
    if (!columns[l]) columns[l] = [];
    columns[l].push(n);
  }
  // Sort each column by (group, id) so nodes sharing a `.Group()`
  // tag land adjacent. Matters for the group-frame overlay: if a
  // group's members are split by an unrelated node of the same
  // topological depth, the bounding box would swallow the outsider.
  // Ungrouped nodes (empty string) sort first within a column.
  for (const col of columns) {
    if (col)
      col.sort((a, b) => {
        const ag = a.groups?.[0] || "";
        const bg = b.groups?.[0] || "";
        if (ag !== bg) return ag.localeCompare(bg);
        return a.id.localeCompare(b.id);
      });
  }

  const pos = new Map<string, { x: number; y: number }>();
  columns.forEach((col, ci) => {
    if (!col) return;
    col.forEach((n, ri) => {
      pos.set(n.id, {
        x: padX + ci * (nodeW + colGap),
        y: padY + ri * (nodeH + rowGap),
      });
    });
  });

  const width =
    padX * 2 +
    Math.max(1, columns.length) * nodeW +
    Math.max(0, columns.length - 1) * colGap;
  const height =
    padY * 2 +
    Math.max(1, ...columns.map((c) => (c ? c.length : 0))) * (nodeH + rowGap);

  const edges: { src: string; dst: string; onFailure?: boolean }[] = [];
  for (const n of nodes) {
    for (const d of n.deps || []) {
      if (byID.has(d)) edges.push({ src: d, dst: n.id });
    }
    if (n.on_failure_of && byID.has(n.on_failure_of)) {
      edges.push({ src: n.on_failure_of, dst: n.id, onFailure: true });
    }
  }

  // Group frames: compute the bounding box around every node sharing
  // the same `.Group("name")` tag so we can draw a labelled dashed
  // container behind them. Rendered before edges/nodes so it sits
  // visually beneath the DAG's active elements. Single-member groups
  // still get a frame so the visual grouping matches the nodes list
  // on the left -- the (safety) header shouldn't look like a
  // different feature from the DAG container.
  const groupFramePad = 8;
  const groupLabelOffset = 6;
  const groupFrames: {
    name: string;
    x: number;
    y: number;
    w: number;
    h: number;
  }[] = [];
  const byGroup = new Map<string, RunNode[]>();
  for (const n of nodes) {
    if (!n.groups || n.groups.length === 0) continue;
    for (const g of n.groups) {
      const arr = byGroup.get(g) ?? [];
      arr.push(n);
      byGroup.set(g, arr);
    }
  }
  for (const [name, members] of byGroup) {
    let minX = Infinity,
      minY = Infinity,
      maxX = -Infinity,
      maxY = -Infinity;
    for (const m of members) {
      const p = pos.get(m.id);
      if (!p) continue;
      if (p.x < minX) minX = p.x;
      if (p.y < minY) minY = p.y;
      if (p.x + nodeW > maxX) maxX = p.x + nodeW;
      if (p.y + nodeH > maxY) maxY = p.y + nodeH;
    }
    if (!isFinite(minX)) continue;
    groupFrames.push({
      name,
      x: minX - groupFramePad,
      y: minY - groupFramePad - groupLabelOffset,
      w: maxX - minX + groupFramePad * 2,
      h: maxY - minY + groupFramePad * 2 + groupLabelOffset,
    });
  }

  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-2 overflow-x-auto">
      <svg
        width={width}
        height={height}
        style={{ minWidth: width, display: "block" }}
      >
        <defs>
          {/* Rainbow gradient for the DYNAMIC pill. Stops mirror the
              subset of `nodePalette` hues the terminal renderer uses
              for its rainbow-letter [dynamic] tag -- keeps the two
              surfaces visually linked. */}
          <linearGradient id="dynamic-pill-grad" x1="0" y1="0" x2="1" y2="0">
            <stop offset="0%" stopColor="#ffaf00" />
            <stop offset="20%" stopColor="#87d7ff" />
            <stop offset="40%" stopColor="#87d787" />
            <stop offset="60%" stopColor="#ff87d7" />
            <stop offset="80%" stopColor="#ff8700" />
            <stop offset="100%" stopColor="#af87d7" />
          </linearGradient>
        </defs>
        {groupFrames.map((g) => (
          <g key={`group-${g.name}`}>
            <rect
              x={g.x}
              y={g.y}
              width={g.w}
              height={g.h}
              rx={8}
              ry={8}
              fill="rgba(148,163,184,0.04)"
              stroke="rgba(148,163,184,0.35)"
              strokeWidth={1}
              strokeDasharray="4 3"
            />
            <text
              x={g.x + 8}
              y={g.y + 10}
              fill="rgba(148,163,184,0.85)"
              fontSize={10}
              fontFamily="ui-monospace, monospace"
            >
              ({g.name})
            </text>
          </g>
        ))}
        {edges.map((e, i) => {
          const a = pos.get(e.src);
          const b = pos.get(e.dst);
          if (!a || !b) return null;
          const x1 = a.x + nodeW;
          const y1 = a.y + nodeH / 2;
          const x2 = b.x;
          const y2 = b.y + nodeH / 2;
          const dx = Math.max(32, (x2 - x1) * 0.4);
          // OnFailure edges get a distinct red-dashed stroke so the
          // reader clocks "this path only runs if the parent failed."
          // Regular deps key off the dst's status (green once
          // succeeded, indigo while running, etc.).
          const color = e.onFailure
            ? "rgba(248,113,113,0.55)"
            : dagEdgeColor(byID.get(e.dst));
          return (
            <path
              key={i}
              d={`M ${x1} ${y1} C ${x1 + dx} ${y1}, ${x2 - dx} ${y2}, ${x2} ${y2}`}
              fill="none"
              stroke={color}
              strokeWidth="1.5"
              strokeDasharray={e.onFailure ? "5 4" : undefined}
            />
          );
        })}
        {nodes.map((n) => {
          const p = pos.get(n.id);
          if (!p) return null;
          const isSel = selected === n.id;
          const { fill, border } = dagNodeColors(n, isSel);
          return (
            <g
              key={n.id}
              transform={`translate(${p.x}, ${p.y})`}
              onClick={() => onSelect(n.id)}
              onMouseEnter={(e) =>
                setHover({ node: n, x: e.clientX, y: e.clientY })
              }
              onMouseMove={(e) =>
                setHover((prev) =>
                  prev && prev.node.id === n.id
                    ? { node: n, x: e.clientX, y: e.clientY }
                    : prev,
                )
              }
              onMouseLeave={() =>
                setHover((prev) =>
                  prev && prev.node.id === n.id ? null : prev,
                )
              }
              style={{ cursor: "pointer" }}
            >
              <rect
                width={nodeW}
                height={nodeH}
                rx={6}
                ry={6}
                fill={fill}
                stroke={border}
                strokeWidth={isSel ? 2 : 1}
              />
              <circle
                cx={14}
                cy={nodeH / 2}
                r={4}
                className={dagStatusClass(n)}
              />
              <text
                x={26}
                y={nodeH / 2 + 4}
                fill="currentColor"
                fontSize={11}
                fontFamily="ui-monospace, monospace"
              >
                {truncate(n.id, 18)}
              </text>
              <text
                x={nodeW - 8}
                y={nodeH / 2 + 4}
                textAnchor="end"
                fill="rgba(148,163,184,0.8)"
                fontSize={10}
                fontFamily="ui-monospace, monospace"
              >
                {fmtMs(nodeDuration(n))}
              </text>
              {n.dynamic && <DynamicPill nodeW={nodeW} />}
              {n.approval && <ApprovalPill n={n} nodeW={nodeW} />}
              {n.outcome === "cached" && !n.approval && (
                <CachedPill nodeW={nodeW} />
              )}
            </g>
          );
        })}
      </svg>
      {hover && <DagNodeTooltip node={hover.node} x={hover.x} y={hover.y} />}
    </div>
  );
}

// DagNodeTooltip is the floating info card shown on DAG-node hover.
// Rendered as a position:fixed sibling of the SVG so it escapes the
// SVG coordinate system and tracks the viewport cursor cleanly.
// Offset 14px down-right of the cursor so it doesn't sit under the
// mouse. Right-anchors when near the viewport edge so the card
// doesn't clip off-screen on rightmost-column hovers.
function DagNodeTooltip({
  node,
  x,
  y,
}: {
  node: RunNode;
  x: number;
  y: number;
}) {
  const state = node.outcome || node.status;
  const runner = parseHolder(node.claimed_by);
  const alignRight = x > window.innerWidth - 280;
  const style: React.CSSProperties = {
    position: "fixed",
    top: y + 14,
    left: alignRight ? undefined : x + 14,
    right: alignRight ? window.innerWidth - x + 14 : undefined,
    zIndex: 100,
    pointerEvents: "none",
  };
  const badges: { label: string; cls: string }[] = [];
  if (node.dynamic)
    badges.push({
      label: "dynamic",
      cls: "bg-fuchsia-500/20 text-fuchsia-300",
    });
  if (node.approval)
    badges.push({
      label: "approval gate",
      cls: "bg-yellow-500/20 text-yellow-300",
    });
  if (node.on_failure_of)
    badges.push({ label: "on failure", cls: "bg-red-500/20 text-red-300" });
  return (
    <div style={style}>
      <div className="bg-[#1e293b] border border-[var(--border)] rounded-lg px-3 py-2 text-xs shadow-xl min-w-[220px] max-w-sm">
        <div className="flex items-center gap-2 mb-1">
          <span
            className={`w-2 h-2 rounded-full shrink-0 ${outcomeDot(node.outcome, node.status)}`}
          />
          <span className="font-mono font-bold">{node.id}</span>
        </div>
        <div className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-[11px]">
          <span className="text-[var(--muted)]">State:</span>
          <span className="font-mono">{state || "pending"}</span>
          <span className="text-[var(--muted)]">Duration:</span>
          <span className="font-mono">{fmtMs(nodeDuration(node))}</span>
          {node.claimed_by && (
            <>
              <span className="text-[var(--muted)]">Runner:</span>
              <span className="font-mono">{runner.label}</span>
            </>
          )}
          {node.groups && node.groups.length > 0 && (
            <>
              <span className="text-[var(--muted)]">
                {node.groups.length === 1 ? "Group:" : "Groups:"}
              </span>
              <span className="font-mono">{node.groups.join(", ")}</span>
            </>
          )}
          {node.on_failure_of && (
            <>
              <span className="text-[var(--muted)]">Fires on fail of:</span>
              <span className="font-mono">{node.on_failure_of}</span>
            </>
          )}
          {node.deps && node.deps.length > 0 && (
            <>
              <span className="text-[var(--muted)]">Deps:</span>
              <span className="font-mono">{node.deps.join(", ")}</span>
            </>
          )}
          {node.failure_reason && (
            <>
              <span className="text-[var(--muted)]">Fail reason:</span>
              <span className="font-mono text-red-300">
                {node.failure_reason}
              </span>
            </>
          )}
        </div>
        {badges.length > 0 && (
          <div className="flex flex-wrap gap-1 mt-2">
            {badges.map((b) => (
              <span
                key={b.label}
                className={`px-1.5 py-0.5 rounded text-[9px] font-bold uppercase tracking-wider ${b.cls}`}
              >
                {b.label}
              </span>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

// DynamicPill is the rainbow-gradient "DYNAMIC" corner badge painted
// on a DAG node whose shape is runtime-variable. Centered along the
// top edge of the node, overhanging upward -- keeps the pill from
// clipping the left or right SVG boundary regardless of column.
function DynamicPill({ nodeW }: { nodeW: number }) {
  const pillW = 56;
  const pillH = 13;
  // Horizontally centered, sitting just above + straddling the top
  // border of the node so it reads as a badge attached to the node
  // rather than a floating annotation.
  const x = (nodeW - pillW) / 2;
  const y = -6;
  return (
    <g style={{ pointerEvents: "none" }}>
      <rect
        x={x}
        y={y}
        width={pillW}
        height={pillH}
        rx={pillH / 2}
        ry={pillH / 2}
        fill="url(#dynamic-pill-grad)"
      />
      <text
        x={x + pillW / 2}
        y={y + pillH / 2 + 3}
        textAnchor="middle"
        fill="white"
        fontSize={9}
        fontWeight={700}
        fontFamily="ui-sans-serif, system-ui, sans-serif"
        style={{
          letterSpacing: "0.5px",
          filter: "drop-shadow(0 0 1px rgba(0,0,0,0.6))",
        }}
      >
        DYNAMIC
      </text>
    </g>
  );
}

// ApprovalPill is the always-on corner badge that tracks an approval
// gate's lifecycle. Three tiers:
//   - grey "APPROVAL" when the gate hasn't been reached yet
//     (node still pending on upstream deps)
//   - amber pulsing "AWAITING" while a human decision is outstanding
//   - solid green "APPROVED" or red "DENIED" once resolved
// Stays visible at every stage so the DAG always shows which nodes
// are human gates, not only when someone is currently blocked.
function ApprovalPill({ n, nodeW }: { n: RunNode; nodeW: number }) {
  const { label, fill, pulse } = approvalPillVisuals(n);
  const pillW = label === "AWAITING" ? 60 : label === "APPROVAL" ? 58 : 64;
  const pillH = 13;
  const x = (nodeW - pillW) / 2;
  const y = -6;
  return (
    <g style={{ pointerEvents: "none" }}>
      <rect
        x={x}
        y={y}
        width={pillW}
        height={pillH}
        rx={pillH / 2}
        ry={pillH / 2}
        fill={fill}
        className={pulse ? "animate-pulse" : undefined}
      />
      <text
        x={x + pillW / 2}
        y={y + pillH / 2 + 3}
        textAnchor="middle"
        fill="rgba(15,15,15,0.95)"
        fontSize={9}
        fontWeight={700}
        fontFamily="ui-sans-serif, system-ui, sans-serif"
        style={{ letterSpacing: "0.5px" }}
      >
        {label}
      </text>
    </g>
  );
}

function approvalPillVisuals(n: RunNode): {
  label: string;
  fill: string;
  pulse: boolean;
} {
  // approval_pending is the canonical "human blocked" state --
  // yellow pulse to match the sidebar's pending-on-humans band.
  if (n.status === "approval_pending") {
    return { label: "AWAITING", fill: "rgba(250,204,21,0.95)", pulse: true };
  }
  // Once the node has an outcome, the gate has been resolved one way
  // or another. Outcome "success" = approval went through (node ran
  // and succeeded). Failed/cancelled = denied or otherwise rejected.
  // Skipped is treated as denied-ish since the node never ran.
  if (n.outcome) {
    switch (n.outcome) {
      case "success":
      case "cached":
        return {
          label: "APPROVED",
          fill: "rgba(74,222,128,0.95)",
          pulse: false,
        };
      case "failed":
      case "cancelled":
      case "skipped":
        return {
          label: "DENIED",
          fill: "rgba(248,113,113,0.95)",
          pulse: false,
        };
    }
  }
  // Gate not yet reached: the node is still pending deps or running
  // its pre-approval work. Grey + no pulse so it reads as "placeholder".
  return { label: "APPROVAL", fill: "rgba(148,163,184,0.75)", pulse: false };
}

// CachedPill signals that a node's output came out of the cache
// instead of being freshly computed. Violet matches the node-rect
// tint for cached outcomes so the pill and body read as one visual
// treatment. Not shown when the node is also an approval gate --
// the ApprovalPill already encodes "APPROVED" for that case and we
// don't want two pills overlapping at the top of the rect.
function CachedPill({ nodeW }: { nodeW: number }) {
  const pillW = 52;
  const pillH = 13;
  const x = (nodeW - pillW) / 2;
  const y = -6;
  return (
    <g style={{ pointerEvents: "none" }}>
      <rect
        x={x}
        y={y}
        width={pillW}
        height={pillH}
        rx={pillH / 2}
        ry={pillH / 2}
        fill="rgba(167,139,250,0.95)"
      />
      <text
        x={x + pillW / 2}
        y={y + pillH / 2 + 3}
        textAnchor="middle"
        fill="rgba(20,15,30,0.95)"
        fontSize={9}
        fontWeight={700}
        fontFamily="ui-sans-serif, system-ui, sans-serif"
        style={{ letterSpacing: "0.5px" }}
      >
        CACHED
      </text>
    </g>
  );
}

function truncate(s: string, n: number): string {
  return s.length <= n ? s : s.slice(0, n - 1) + "…";
}

function dagEdgeColor(dst?: RunNode): string {
  if (!dst) return "rgba(107,114,128,0.35)";
  const k = dst.outcome || dst.status;
  switch (k) {
    case "success":
      return "rgba(74,222,128,0.45)";
    case "failed":
      return "rgba(248,113,113,0.5)";
    case "running":
    case "claimed":
      return "rgba(129,140,248,0.5)";
    case "cancelled":
      return "rgba(148,163,184,0.4)";
    case "approval_pending":
      return "rgba(250,204,21,0.6)";
    default:
      return "rgba(148,163,184,0.35)";
  }
}

function dagNodeColors(
  n: RunNode,
  isSelected: boolean,
): { fill: string; border: string } {
  const k = n.outcome || n.status;
  let fill = "rgba(100,116,139,0.12)";
  let border = "rgba(100,116,139,0.35)";
  switch (k) {
    case "success":
      fill = "rgba(34,197,94,0.10)";
      border = "rgba(74,222,128,0.45)";
      break;
    case "failed":
      fill = "rgba(239,68,68,0.12)";
      border = "rgba(248,113,113,0.55)";
      break;
    case "running":
    case "claimed":
      fill = "rgba(99,102,241,0.12)";
      border = "rgba(129,140,248,0.55)";
      break;
    case "cancelled":
      fill = "rgba(100,116,139,0.10)";
      border = "rgba(148,163,184,0.5)";
      break;
    case "cached":
      fill = "rgba(139,92,246,0.12)";
      border = "rgba(167,139,250,0.55)";
      break;
    case "skipped":
      fill = "rgba(100,116,139,0.08)";
      border = "rgba(100,116,139,0.3)";
      break;
    case "skipped-concurrent":
      // OnLimit:Skip. A darker slate than plain skipped so
      // operators can spot "slot was full" in the DAG.
      fill = "rgba(71,85,105,0.14)";
      border = "rgba(100,116,139,0.5)";
      break;
    case "superseded":
      // CancelOthers eviction. Amber (distinct from
      // cancelled's slate) signals "replaced by newer run."
      fill = "rgba(245,158,11,0.14)";
      border = "rgba(251,191,36,0.7)";
      break;
    case "approval_pending":
      // Yellow pulse, matching the "waiting on humans / resources"
      // band used for pending in the sidebar. Keeps the gate visually
      // distinct from cancelled (slate) and cached (violet).
      fill = "rgba(250,204,21,0.14)";
      border = "rgba(250,204,21,0.8)";
      break;
  }
  if (isSelected) border = "rgba(251,191,36,0.9)";
  return { fill, border };
}

function dagStatusClass(n: RunNode): string {
  const k = n.outcome || n.status;
  switch (k) {
    case "success":
      return "fill-green-400";
    case "failed":
      return "fill-red-400";
    case "running":
    case "claimed":
      return "fill-indigo-400";
    case "cancelled":
      return "fill-slate-500";
    case "cached":
      return "fill-violet-400";
    case "skipped":
      return "fill-slate-500";
    case "skipped-concurrent":
      return "fill-slate-600";
    case "superseded":
      return "fill-amber-500";
    case "approval_pending":
      return "fill-yellow-400 animate-pulse";
    default:
      return "fill-gray-500";
  }
}

// --- action buttons ---

function CancelButton({
  runId,
  onDone,
}: {
  runId: string;
  onDone: () => void;
}) {
  const [loading, setLoading] = useState(false);
  return (
    <button
      onClick={async (e) => {
        e.stopPropagation();
        if (!confirm(`Cancel run ${runId}?`)) return;
        setLoading(true);
        await cancelRun(runId);
        onDone();
        setLoading(false);
      }}
      disabled={loading}
      className="bg-red-500/20 text-red-400 border border-red-500/30 px-2 py-1 rounded text-xs font-medium hover:bg-red-500/30 transition-colors"
    >
      {loading ? "..." : "Cancel"}
    </button>
  );
}

function RetryButton({ runId, onDone }: { runId: string; onDone: () => void }) {
  const [loading, setLoading] = useState(false);
  return (
    <button
      onClick={async (e) => {
        e.stopPropagation();
        setLoading(true);
        await retryRun(runId);
        onDone();
        setLoading(false);
      }}
      disabled={loading}
      className="bg-indigo-500/20 text-indigo-400 border border-indigo-500/30 px-2 py-1 rounded text-xs font-medium hover:bg-indigo-500/30 transition-colors"
    >
      {loading ? "..." : "Rerun"}
    </button>
  );
}
