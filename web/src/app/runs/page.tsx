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

import {
  Suspense,
  memo,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useRouter, useSearchParams } from "next/navigation";
import PipelineOverview from "@/components/PipelineOverview";
import {
  type FilterCtx,
  type FilterFacet,
  FilterableValue,
  FullFilterBar,
  activeFilterCount,
  buildGroupsFromState,
  clearAllFilters,
  computeOptions,
  repoLabel,
  runMatchesFilter,
  useFilterCtx,
  useFilterDropdownState,
  useUrlFilterState,
} from "@/components/RunFilters";
import {
  type Node as RunNode,
  type NodeWorkStep,
  type SpawnedPipelineRef,
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
import {
  fmtAgo,
  fmtAgoShort,
  fmtClock,
  fmtDatePrefix,
  fmtFullDate,
  fmtMs,
  fmtMsCompact,
} from "@/lib/timeFormat";
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
      <RunsRoute />
    </Suspense>
  );
}

function RunsRoute() {
  const searchParams = useSearchParams();
  const router = useRouter();
  const view: "activity" | "pipelines" =
    searchParams.get("view") === "pipelines" ? "pipelines" : "activity";

  const setView = (next: "activity" | "pipelines") => {
    const params = new URLSearchParams(searchParams.toString());
    if (next === "pipelines") {
      params.set("view", "pipelines");
      params.delete("node");
    } else {
      params.delete("view");
    }
    const qs = params.toString();
    router.push(qs ? `/runs?${qs}` : "/runs");
  };

  const pivotTabs = (
    <div className="flex items-center gap-1 shrink-0">
      <PivotTab
        label="Activity"
        active={view === "activity"}
        onClick={() => setView("activity")}
      />
      <PivotTab
        label="By pipeline"
        active={view === "pipelines"}
        onClick={() => setView("pipelines")}
      />
      <span className="mx-2 h-5 w-px bg-[var(--border)]" />
    </div>
  );

  return view === "pipelines" ? (
    <PipelineOverview pivotTabs={pivotTabs} />
  ) : (
    <Pipelines pivotTabs={pivotTabs} />
  );
}

function PivotTab({
  label,
  active,
  onClick,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      className={`text-xs px-3 py-2 border-b-2 transition-colors -mb-px ${
        active
          ? "border-cyan-400 text-[var(--foreground)] font-semibold"
          : "border-transparent text-[var(--muted)] hover:text-[var(--foreground)]"
      }`}
    >
      {label}
    </button>
  );
}

function Pipelines({ pivotTabs }: { pivotTabs: React.ReactNode }) {
  const searchParams = useSearchParams();
  const router = useRouter();
  const [runs, setRuns] = useState<Run[]>([]);
  const [pipelineMeta, setPipelineMeta] = useState<
    Record<string, PipelineMeta>
  >({});
  const [detail, setDetail] = useState<RunDetail | null>(null);
  const initialRun = searchParams.get("run");
  const [selectedRun, setSelectedRun] = useState<string | null>(initialRun);
  // checkedRuns is the selection set — what rerun / delete operate
  // on. The detail pane (selectedRun) is a separate "viewing" state;
  // opening a detail also adds that run to the selection so the user
  // sees what's selected, but un-viewing doesn't drop it from the set.
  const [checkedRuns, setCheckedRuns] = useState<Set<string>>(() =>
    initialRun ? new Set([initialRun]) : new Set(),
  );
  const toggleChecked = (id: string) => {
    setCheckedRuns((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };
  const [selectedNode, setSelectedNode] = useState<string | null>(null);
  // Selected step id within the selected node (or null if no step is
  // currently focused). Shared across:
  //   - the left Nodes panel (highlights the row + scrolls into view)
  //   - the StepDag (paints the step gold)
  //   - the Logs tab (expands+scrolls to the step bucket)
  // Cleared when the selected node changes via selectNode.
  const [selectedStep, setSelectedStep] = useState<string | null>(null);
  // Wrappers so callers don't have to remember to coordinate the two
  // pieces of state. selectNode clears any step focus; selectStep
  // assigns both at once so the post-render reads them consistently.
  const selectNode = useCallback((id: string | null) => {
    setSelectedNode(id);
    setSelectedStep(null);
  }, []);
  const selectStep = useCallback((nodeId: string, stepId: string | null) => {
    setSelectedNode(nodeId);
    setSelectedStep(stepId);
  }, []);
  const filterState = useUrlFilterState();
  const { openDropdown, setOpenDropdown, filterRef } = useFilterDropdownState();
  const [showTrigger, setShowTrigger] = useState(false);

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

  // Live progress for running runs. Fetches detail for each running
  // run on every poll cycle and caches node counts so the list can
  // show "3/8" badges without opening the detail pane. Skipped when
  // there are no running runs to keep network quiet.
  const [runProgress, setRunProgress] = useState<
    Record<string, { done: number; total: number }>
  >({});
  useEffect(() => {
    let cancelled = false;
    const running = runs.filter((r) => r.status === "running");
    if (running.length === 0) return;
    const fetchAll = async () => {
      const updates = await Promise.all(
        running.map(async (r) => {
          const d = await getRun(r.id);
          if (!d) return null;
          const total = d.nodes.length;
          const done = d.nodes.filter((n) =>
            ["success", "complete", "failed", "cancelled"].includes(n.status),
          ).length;
          return [r.id, { done, total }] as const;
        }),
      );
      if (cancelled) return;
      setRunProgress((prev) => {
        const next = { ...prev };
        for (const u of updates) if (u) next[u[0]] = u[1];
        return next;
      });
    };
    fetchAll();
    return () => {
      cancelled = true;
    };
  }, [runs]);

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

  const options = computeOptions(runs, pipelineMeta);
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
  const topLevel = useMemo(
    () =>
      runs
        .filter((r) => runMatchesFilter(r, filterState, pipelineMeta))
        .sort(
          (a, b) =>
            new Date(b.started_at).getTime() - new Date(a.started_at).getTime(),
        ),
    [runs, filterState, pipelineMeta],
  );
  const activeCount = activeFilterCount(filterState);

  // When a run is selected via URL (typically arriving from the By
  // pipeline view) and the row mounts in topLevel, scroll it into
  // view once. Tracked per-id so polls don't keep re-scrolling.
  const scrolledForRef = useRef<string | null>(null);
  useEffect(() => {
    if (!selectedRun) return;
    if (scrolledForRef.current === selectedRun) return;
    if (!topLevel.some((r) => r.id === selectedRun)) return;
    const el = document.querySelector(
      `[data-run-id="${selectedRun}"]`,
    ) as HTMLElement | null;
    if (!el) return;
    scrolledForRef.current = selectedRun;
    el.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [selectedRun, topLevel]);

  const run = detail?.run || null;
  const nodes = detail?.nodes || [];
  const node = nodes.find((n) => n.id === selectedNode) || null;

  const filterCtx = useFilterCtx(filterState);

  // Keyboard cursor: j/down moves to next row, k/up to previous,
  // Enter opens the focused run, Esc closes the detail (or clears
  // focus when nothing is open). Cursor is separate from selectedRun
  // so the user can scroll through rows without fetching detail on
  // every keystroke.
  const [focusedRun, setFocusedRun] = useState<string | null>(null);
  const [focusedNode, setFocusedNode] = useState<string | null>(null);
  const [focusedColumn, setFocusedColumn] = useState<"runs" | "nodes" | "tabs">(
    "runs",
  );
  const [tab, setTab] = useState<TabKey>("summary");
  const [focusedTab, setFocusedTab] = useState<TabKey | null>(null);
  const topLevelRef = useRef(topLevel);
  topLevelRef.current = topLevel;
  const nodesRef = useRef(nodes);
  nodesRef.current = nodes;
  const focusedRunRef = useRef(focusedRun);
  focusedRunRef.current = focusedRun;
  const focusedNodeRef = useRef(focusedNode);
  focusedNodeRef.current = focusedNode;
  const focusedColumnRef = useRef(focusedColumn);
  focusedColumnRef.current = focusedColumn;
  const focusedTabRef = useRef(focusedTab);
  focusedTabRef.current = focusedTab;
  const tabRef = useRef(tab);
  tabRef.current = tab;
  const visibleTabs = useMemo(() => buildVisibleTabs(nodes), [nodes]);
  const visibleTabsRef = useRef(visibleTabs);
  visibleTabsRef.current = visibleTabs;
  const selectedRunRef = useRef(selectedRun);
  selectedRunRef.current = selectedRun;
  const selectedNodeRef = useRef(selectedNode);
  selectedNodeRef.current = selectedNode;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null;
      const tag = t?.tagName;
      if (
        tag === "INPUT" ||
        tag === "TEXTAREA" ||
        tag === "SELECT" ||
        t?.isContentEditable
      )
        return;
      const runs = topLevelRef.current;
      const ns = nodesRef.current;
      const col = focusedColumnRef.current;
      const tabs = visibleTabsRef.current;
      // ── Column transitions ──────────────────────────────────────
      if (e.key === "h" || e.key === "ArrowLeft") {
        e.preventDefault();
        if (col === "tabs") {
          const ft = focusedTabRef.current;
          const idx = ft ? tabs.findIndex((t) => t.key === ft) : -1;
          if (idx > 0) {
            setFocusedTab(tabs[idx - 1].key);
            return;
          }
          // At first tab → step left into nodes column.
          setFocusedColumn(ns.length > 0 ? "nodes" : "runs");
          setFocusedTab(null);
          return;
        }
        if (col === "nodes") {
          setFocusedColumn("runs");
          return;
        }
        // Already in runs column — no-op.
        return;
      }
      if (e.key === "l" || e.key === "ArrowRight") {
        if (!selectedRunRef.current) return;
        e.preventDefault();
        if (col === "tabs") {
          const ft = focusedTabRef.current;
          const idx = ft ? tabs.findIndex((t) => t.key === ft) : -1;
          if (idx < tabs.length - 1)
            setFocusedTab(tabs[idx + 1]?.key ?? tabs[0]?.key ?? null);
          return;
        }
        if (col === "runs") {
          if (ns.length > 0) {
            setFocusedColumn("nodes");
            if (!focusedNodeRef.current) setFocusedNode(ns[0].id);
          } else if (tabs.length > 0) {
            setFocusedColumn("tabs");
            setFocusedTab(tabs[0].key);
          }
          return;
        }
        if (col === "nodes") {
          if (tabs.length > 0) {
            setFocusedColumn("tabs");
            setFocusedTab(tabs[0].key);
          }
          return;
        }
      }
      // ── Per-column j/k/Enter/Escape ─────────────────────────────
      if (col === "tabs" && tabs.length > 0) {
        if (e.key === "Enter") {
          e.preventDefault();
          if (focusedTabRef.current) setTab(focusedTabRef.current);
          return;
        }
        if (e.key === "Escape") {
          setFocusedColumn(ns.length > 0 ? "nodes" : "runs");
          setFocusedTab(null);
          return;
        }
        // j/k are no-ops in tabs column.
        return;
      }
      if (col === "nodes" && ns.length > 0) {
        const cur =
          focusedNodeRef.current ?? selectedNodeRef.current ?? ns[0].id;
        const idx = ns.findIndex((n) => n.id === cur);
        if (e.key === "j" || e.key === "ArrowDown") {
          e.preventDefault();
          // Cycle: header → first → ... → last → header
          if (!focusedNodeRef.current) setFocusedNode(ns[0].id);
          else if (idx >= ns.length - 1) setFocusedNode(null);
          else setFocusedNode(ns[idx + 1].id);
        } else if (e.key === "k" || e.key === "ArrowUp") {
          e.preventDefault();
          // Cycle: header → last → ... → first → header
          if (!focusedNodeRef.current) setFocusedNode(ns[ns.length - 1].id);
          else if (idx <= 0) setFocusedNode(null);
          else setFocusedNode(ns[idx - 1].id);
        } else if (e.key === "Enter") {
          e.preventDefault();
          // Header parked (no node focus): Enter clears selection.
          if (!focusedNodeRef.current) {
            selectNode(null);
            return;
          }
          selectNode(focusedNodeRef.current);
        } else if (e.key === "Escape") {
          if (selectedNodeRef.current) {
            selectNode(null);
          } else if (selectedRunRef.current) {
            setFocusedColumn("runs");
            selectRunRef.current(null);
          } else {
            setFocusedColumn("runs");
            setFocusedNode(null);
          }
        }
        return;
      }
      if (runs.length === 0) return;
      const cur = focusedRunRef.current ?? selectedRunRef.current ?? runs[0].id;
      const idx = runs.findIndex((r) => r.id === cur);
      if (e.key === "j" || e.key === "ArrowDown") {
        e.preventDefault();
        const i = idx < 0 ? 0 : (idx + 1) % runs.length;
        setFocusedRun(runs[i].id);
      } else if (e.key === "k" || e.key === "ArrowUp") {
        e.preventDefault();
        const i =
          idx < 0 ? runs.length - 1 : (idx - 1 + runs.length) % runs.length;
        setFocusedRun(runs[i].id);
      } else if (e.key === "Enter") {
        e.preventDefault();
        const target = focusedRunRef.current ?? runs[0].id;
        if (!target) return;
        if (selectedRunRef.current === target) selectRunRef.current(null);
        else selectRunRef.current(target);
      } else if (e.key === "Escape") {
        if (selectedRunRef.current) selectRunRef.current(null);
        else setFocusedRun(null);
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  // Scroll focused row into view when it changes via keyboard.
  useEffect(() => {
    if (!focusedRun) return;
    const el = document.querySelector(
      `[data-run-id="${focusedRun}"]`,
    ) as HTMLElement | null;
    if (!el) return;
    el.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [focusedRun]);

  // Same for focused node.
  useEffect(() => {
    if (!focusedNode) return;
    const el = document.querySelector(
      `[data-node-id="${focusedNode}"]`,
    ) as HTMLElement | null;
    if (!el) return;
    el.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [focusedNode]);

  // Scroll focused tab into view.
  useEffect(() => {
    if (!focusedTab) return;
    const el = document.querySelector(
      `[data-tab-key="${focusedTab}"]`,
    ) as HTMLElement | null;
    el?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [focusedTab]);

  // When a node selection clears, drop the keyboard focus off any
  // specific node so the cursor parks on the Nodes header instead of
  // staying on whatever was just deselected.
  const prevSelectedNodeRef = useRef(selectedNode);
  useEffect(() => {
    if (prevSelectedNodeRef.current && !selectedNode) setFocusedNode(null);
    prevSelectedNodeRef.current = selectedNode;
  }, [selectedNode]);

  const selectRun = (id: string | null) => {
    setSelectedRun(id);
    selectNode(null);
    // Row body click is single-select: replace the selection set so
    // only this row is highlighted. When exiting the collapsed view
    // (id=null), keep the previous selection in checkedRuns so the
    // user can spot where they came from on the expanded list.
    if (id) setCheckedRuns(new Set([id]));
    const params = new URLSearchParams(searchParams.toString());
    if (id) params.set("run", id);
    else params.delete("run");
    const qs = params.toString();
    router.replace(qs ? `/runs?${qs}` : "/runs", { scroll: false });
  };
  const selectRunRef = useRef(selectRun);
  selectRunRef.current = selectRun;

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
          trailingActions={
            <div className="flex items-center gap-2">
              {topLevel.length > 0 && (
                <label className="flex items-center gap-1.5 text-[10px] text-[var(--muted)] cursor-pointer shrink-0">
                  <input
                    type="checkbox"
                    ref={(el) => {
                      if (!el) return;
                      el.indeterminate =
                        checkedRuns.size > 0 &&
                        !topLevel.every((r) => checkedRuns.has(r.id));
                    }}
                    checked={checkedRuns.size > 0}
                    onChange={() => {
                      if (checkedRuns.size > 0) setCheckedRuns(new Set());
                      else setCheckedRuns(new Set(topLevel.map((r) => r.id)));
                    }}
                    aria-label="select all"
                    className="cursor-pointer accent-violet-500"
                  />
                  <span>
                    {checkedRuns.size > 0
                      ? `${checkedRuns.size} of ${topLevel.length} selected`
                      : `${topLevel.length} runs`}
                  </span>
                </label>
              )}
              <button
                disabled={checkedRuns.size !== 1}
                onClick={async () => {
                  if (checkedRuns.size !== 1) return;
                  const [id] = checkedRuns;
                  await retryRun(id).catch(() => null);
                  refresh();
                }}
                title={
                  checkedRuns.size === 0
                    ? "Select a run to rerun"
                    : checkedRuns.size > 1
                      ? "Rerun supports one run at a time"
                      : `Rerun ${[...checkedRuns][0]}`
                }
                className="text-[10px] px-2 py-1 rounded border border-[var(--border)] text-[var(--muted)] hover:text-[var(--foreground)] hover:border-[var(--foreground)] disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:text-[var(--muted)] disabled:hover:border-[var(--border)] transition-colors"
              >
                ↻ Rerun
              </button>
              <button
                disabled
                title={
                  checkedRuns.size > 0
                    ? `Delete ${checkedRuns.size} run${checkedRuns.size === 1 ? "" : "s"} (not implemented yet)`
                    : "Delete (not implemented yet)"
                }
                className="text-[10px] px-2 py-1 rounded border border-[var(--border)] text-[var(--muted)] opacity-40 cursor-not-allowed"
              >
                ✕ Delete
              </button>
            </div>
          }
        />
      </div>

      <div className="flex flex-1 overflow-hidden">
        {/* Left: Runs list. Collapses to a sidebar when a run is
          selected; expands to fill the screen otherwise. */}
        <div
          className={`${run ? "w-52 shrink-0" : "flex-1"} border-r border-[var(--border)] flex flex-col transition-all`}
        >
          <div className="flex-1 overflow-y-auto">
            {topLevel.map((r) => {
              const isActive = selectedRun === r.id;
              const isChecked = checkedRuns.has(r.id);
              const isFocused = focusedRun === r.id;
              const isActiveFocus = isFocused && focusedColumn === "runs";
              return (
                <div
                  key={r.id}
                  data-run-id={r.id}
                  onClick={() => {
                    setFocusedRun(r.id);
                    setFocusedColumn("runs");
                    selectRun(isActive ? null : r.id);
                  }}
                  className={`px-3 py-2 border-b border-[var(--border)] border-l-4 cursor-pointer hover:bg-[var(--surface-raised)] transition-colors flex items-start gap-2 ${
                    isChecked
                      ? "bg-violet-500/15 border-l-violet-400"
                      : "border-l-transparent"
                  } ${
                    isActiveFocus
                      ? "ring-2 ring-inset ring-violet-300 bg-violet-500/10"
                      : isFocused
                        ? "ring-1 ring-inset ring-violet-400/30"
                        : ""
                  }`}
                >
                  {!run && (
                    <label
                      onClick={(e) => e.stopPropagation()}
                      className="-m-2 p-2 shrink-0 cursor-pointer flex items-start"
                      title="select run"
                    >
                      <input
                        type="checkbox"
                        checked={isChecked}
                        onChange={() => toggleChecked(r.id)}
                        aria-label="select run"
                        className="mt-1 cursor-pointer accent-violet-500"
                      />
                    </label>
                  )}
                  <div className="flex-1 min-w-0">
                    <FullRunRow
                      r={r}
                      ctx={filterCtx}
                      compact={!!run}
                      progress={runProgress[r.id]}
                    />
                  </div>
                </div>
              );
            })}
            {topLevel.length === 0 && (
              <div className="p-8 text-center text-[var(--muted)] text-sm">
                {activeCount > 0 ? "No matching runs" : "No runs yet"}
              </div>
            )}
          </div>
        </div>

        {/* Middle: RunNodes in run */}
        {run && detail && (
          <div className="w-44 border-r border-[var(--border)] flex flex-col shrink-0 overflow-y-auto">
            <div
              onClick={() => {
                setFocusedColumn("nodes");
                setFocusedNode(null);
                selectNode(null);
              }}
              className={`px-3 py-2 border-b border-[var(--border)] flex items-center justify-between text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] cursor-pointer hover:bg-[var(--surface-raised)] transition-colors ${
                focusedColumn === "nodes" && !focusedNode
                  ? "ring-2 ring-inset ring-indigo-300 bg-indigo-500/10"
                  : ""
              }`}
              title="click to deselect node"
            >
              <span>Nodes ({nodes.length})</span>
              {selectedNode && (
                <button
                  onClick={(e) => {
                    e.stopPropagation();
                    selectNode(null);
                  }}
                  className="text-[var(--muted)] hover:text-red-400 normal-case font-normal tracking-normal"
                  title="clear node selection"
                >
                  deselect ×
                </button>
              )}
            </div>
            <NodesList
              nodes={nodes}
              selectedNode={selectedNode}
              selectedStep={selectedStep}
              focusedNode={focusedNode}
              focusedColumnActive={focusedColumn === "nodes"}
              onSelect={(id) => {
                setFocusedNode(id);
                setFocusedColumn("nodes");
                selectNode(id);
              }}
              onSelectStep={(nodeId, stepId) => {
                setFocusedNode(nodeId);
                setFocusedColumn("nodes");
                selectStep(nodeId, stepId);
              }}
            />
          </div>
        )}

        {/* Right: detail + logs. Hidden until a run is selected so the
          runs list above can spread across the full viewport. */}
        {run && (
          <div className="flex-1 flex flex-col overflow-hidden">
            <RunDetailPane
              run={run}
              nodes={nodes}
              node={node}
              selectedStep={selectedStep}
              showTrigger={showTrigger}
              setShowTrigger={setShowTrigger}
              onSelectNode={selectNode}
              onSelectStep={selectStep}
              tab={tab}
              setTab={setTab}
              focusedTab={focusedColumn === "tabs" ? focusedTab : null}
              onRefresh={() => {
                refresh();
                if (selectedRun) loadDetail(selectedRun);
              }}
            />
          </div>
        )}
      </div>
    </div>
  );
}

function useClickPopup<T extends HTMLElement>() {
  const [open, setOpen] = useState(false);
  const ref = useRef<T>(null);
  useEffect(() => {
    if (!open) return;
    const onDocClick = (e: MouseEvent) => {
      if (!ref.current || ref.current.contains(e.target as Node)) return;
      e.stopPropagation();
      e.preventDefault();
      setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("click", onDocClick, true);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("click", onDocClick, true);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);
  return { open, setOpen, ref };
}

function FilterableTimestamp({
  iso,
  field,
  ctx,
  tooltip,
  children,
}: {
  iso: string;
  field: "started" | "finished";
  ctx: FilterCtx;
  tooltip?: string;
  children: React.ReactNode;
}) {
  const { open, setOpen, ref } = useClickPopup<HTMLSpanElement>();
  return (
    <span
      ref={ref}
      className="relative inline-flex items-center"
      onClick={(e) => {
        e.stopPropagation();
        setOpen((o) => !o);
      }}
    >
      <span className="cursor-pointer rounded px-1 -mx-1 transition-colors hover:bg-[var(--surface-raised)] hover:underline hover:decoration-dotted hover:decoration-[var(--muted)] hover:underline-offset-4">
        {tooltip ? <Tooltip content={tooltip}>{children}</Tooltip> : children}
      </span>
      {open && (
        <span className="absolute top-full left-0 mt-1 flex flex-col gap-0.5 z-50 bg-[var(--surface)] border border-[var(--border)] rounded p-1 shadow-lg whitespace-nowrap text-[10px] min-w-[160px]">
          <button
            onClick={(e) => {
              e.stopPropagation();
              ctx.setDateBound(field, "before", iso);
              setOpen(false);
            }}
            className="px-2 py-0.5 rounded text-left hover:bg-[var(--surface-raised)] text-[var(--muted)] hover:text-orange-300"
          >
            + set as &apos;{field} before&apos;
          </button>
          <button
            onClick={(e) => {
              e.stopPropagation();
              ctx.setDateBound(field, "after", iso);
              setOpen(false);
            }}
            className="px-2 py-0.5 rounded text-left hover:bg-[var(--surface-raised)] text-[var(--muted)] hover:text-orange-300"
          >
            + set as &apos;{field} after&apos;
          </button>
          <button
            onClick={(e) => {
              e.stopPropagation();
              navigator.clipboard.writeText(iso);
              setOpen(false);
            }}
            className="px-2 py-0.5 rounded text-left text-[var(--muted)] hover:bg-[var(--surface-raised)] hover:text-[var(--foreground)] border-t border-[var(--border)] mt-0.5 pt-1"
          >
            ⧉ copy
          </button>
        </span>
      )}
    </span>
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
  selectedStep,
  focusedNode,
  focusedColumnActive,
  onSelect,
  onSelectStep,
}: {
  nodes: RunNode[];
  selectedNode: string | null;
  selectedStep: string | null;
  focusedNode?: string | null;
  focusedColumnActive?: boolean;
  onSelect: (id: string | null) => void;
  onSelectStep: (nodeId: string, stepId: string | null) => void;
}) {
  const groups = partitionByGroup(nodes);
  // Collapse state is keyed on the group name and driven by the
  // design-doc default: expanded while anything's still moving or
  // failed; collapsed once every child succeeded. The user can
  // override either way by clicking the header; we track that as an
  // explicit toggle so auto-collapse doesn't fight them.
  // Groups default to expanded so every node is visible without
  // hunting; the user can collapse one explicitly via the header
  // chevron. Tracked as a Set so the default state is "open".
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(
    new Set(),
  );
  const toggle = (g: string) =>
    setCollapsedGroups((prev) => {
      const next = new Set(prev);
      if (next.has(g)) next.delete(g);
      else next.add(g);
      return next;
    });
  // Per-node step expansion. Default collapsed so the panel stays
  // dense; user clicks the caret to drill into a node's steps. Also
  // auto-expands when a step gets selected elsewhere (StepDag click,
  // logs nav) so the row reveals its children without manual toggle.
  const [expandedNodes, setExpandedNodes] = useState<Set<string>>(new Set());
  const toggleNode = (id: string) =>
    setExpandedNodes((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  useEffect(() => {
    if (!selectedStep || !selectedNode) return;
    setExpandedNodes((prev) => {
      if (prev.has(selectedNode)) return prev;
      const next = new Set(prev);
      next.add(selectedNode);
      return next;
    });
  }, [selectedStep, selectedNode]);

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
              selectedStep={selectedNode === n.id ? selectedStep : null}
              focused={focusedNode === n.id}
              focusedColumnActive={focusedColumnActive}
              expanded={expandedNodes.has(n.id)}
              onToggleExpand={() => toggleNode(n.id)}
              onSelect={onSelect}
              onSelectStep={onSelectStep}
            />
          ));
        }
        const agg = aggregateGroupStatus(children);
        const collapsed = collapsedGroups.has(group);
        const accent = groupAccentClass(group);
        return (
          <div key={group} className="relative">
            <GroupHeader
              name={group}
              agg={agg}
              count={children.length}
              collapsed={collapsed}
              onToggle={() => toggle(group)}
              accentClass={accent}
            />
            {!collapsed && (
              <div className="relative">
                <span
                  className={`absolute left-0 top-0 bottom-0 w-0.5 ${accent}`}
                />
                {children.map((n) => (
                  <NodeRow
                    key={n.id}
                    n={n}
                    selected={selectedNode === n.id}
                    selectedStep={selectedNode === n.id ? selectedStep : null}
                    focused={focusedNode === n.id}
                    focusedColumnActive={focusedColumnActive}
                    expanded={expandedNodes.has(n.id)}
                    onToggleExpand={() => toggleNode(n.id)}
                    onSelect={onSelect}
                    onSelectStep={onSelectStep}
                  />
                ))}
              </div>
            )}
          </div>
        );
      })}
    </>
  );
}

// groupAccentClass picks a stable Tailwind background class for the
// vertical bar marking nodes in a group. Hashes by group name so the
// same group keeps the same color across renders.
function groupAccentClass(name: string): string {
  const palette = [
    "bg-cyan-400/70",
    "bg-amber-400/70",
    "bg-pink-400/70",
    "bg-emerald-400/70",
    "bg-violet-400/70",
    "bg-sky-400/70",
  ];
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) | 0;
  return palette[Math.abs(h) % palette.length];
}

function GroupHeader({
  name,
  agg,
  count,
  collapsed,
  onToggle,
  accentClass,
}: {
  name: string;
  agg: GroupAgg;
  count: number;
  collapsed: boolean;
  onToggle: () => void;
  accentClass?: string;
}) {
  return (
    <button
      onClick={onToggle}
      className="relative w-full flex items-center gap-2 px-3 py-2 border-b border-[var(--border)] text-left hover:bg-[var(--surface-raised)] transition-colors"
    >
      {accentClass && (
        <span
          className={`absolute left-0 top-0 bottom-0 w-0.5 ${accentClass}`}
        />
      )}
      <span className="w-3 text-center text-[var(--muted)] text-[10px]">
        {collapsed ? "▸" : "▾"}
      </span>
      <Tooltip content={`${agg} (${count} node${count === 1 ? "" : "s"})`}>
        <span className={`w-2 h-2 rounded-full shrink-0 ${statusDot(agg)}`} />
      </Tooltip>
      <span className="text-xs text-[var(--foreground)] truncate font-semibold">
        {name}
      </span>
      <span className="ml-auto shrink-0">
        <Tooltip content={`${count} node${count === 1 ? "" : "s"} in group`}>
          <span className="text-[10px] font-mono text-[var(--muted)]">
            {count}
          </span>
        </Tooltip>
      </span>
    </button>
  );
}

function NodeRow({
  n,
  selected,
  selectedStep,
  focused,
  focusedColumnActive,
  indent,
  expanded,
  onToggleExpand,
  onSelect,
  onSelectStep,
}: {
  n: RunNode;
  selected: boolean;
  selectedStep?: string | null;
  focused?: boolean;
  focusedColumnActive?: boolean;
  indent?: boolean;
  expanded?: boolean;
  onToggleExpand?: () => void;
  onSelect: (id: string | null) => void;
  onSelectStep?: (nodeId: string, stepId: string | null) => void;
}) {
  const label = n.id.length > 20 ? n.id.slice(0, 19) + "…" : n.id;
  const statusLabel = n.outcome || n.status;
  const steps = n.work?.steps ?? [];
  const hasSteps = steps.length > 0;
  return (
    <>
      <div
        data-node-id={n.id}
        className={`${indent ? "pl-4 pr-2" : "px-2"} py-1.5 border-b border-[var(--border)] border-l-4 cursor-pointer hover:bg-[var(--surface-raised)] transition-colors ${
          selected
            ? "bg-violet-500/15 border-l-violet-400"
            : "border-l-transparent"
        } ${
          focused && focusedColumnActive
            ? "ring-2 ring-inset ring-indigo-300 bg-indigo-500/10"
            : focused
              ? "ring-1 ring-inset ring-indigo-400/30"
              : ""
        }`}
        onClick={() => onSelect(selected ? null : n.id)}
        title={`${n.id} · ${statusLabel}${nodeDuration(n) ? ` · ${fmtMs(nodeDuration(n))}` : ""}`}
      >
        <div className="flex items-center gap-1.5">
          {hasSteps ? (
            <button
              onClick={(e) => {
                e.stopPropagation();
                onToggleExpand?.();
              }}
              className="w-3 text-center text-[10px] text-[var(--muted)] hover:text-[var(--foreground)] shrink-0"
              title={expanded ? "collapse steps" : "expand steps"}
            >
              {expanded ? "▾" : "▸"}
            </button>
          ) : (
            <span className="w-3 shrink-0" />
          )}
          <span
            className={`w-2 h-2 rounded-full shrink-0 ${outcomeDot(n.outcome, n.status)}`}
          />
          <span className="text-[11px] truncate flex-1 min-w-0">{label}</span>
          {(n.annotations?.length ?? 0) > 0 && (
            <span
              className="text-[10px] font-mono text-cyan-300 shrink-0"
              title={`${n.annotations!.length} annotation${n.annotations!.length === 1 ? "" : "s"}`}
            >
              {n.annotations!.length}
            </span>
          )}
          {nodeDuration(n) > 0 && (
            <span className="text-[10px] font-mono text-[var(--muted)] shrink-0 tabular-nums">
              {fmtMs(nodeDuration(n))}
            </span>
          )}
        </div>
      </div>
      {hasSteps && expanded && (
        <div className="bg-[#0a0f17]">
          {steps.map((s) => (
            <StepRow
              key={s.id}
              s={s}
              selected={selectedStep === s.id}
              onClick={() => {
                const isSel = selectedStep === s.id;
                if (onSelectStep) onSelectStep(n.id, isSel ? null : s.id);
                else onSelect(n.id);
              }}
            />
          ))}
        </div>
      )}
    </>
  );
}

function StepRow({
  s,
  selected,
  onClick,
}: {
  s: NodeWorkStep;
  selected?: boolean;
  onClick: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (selected) {
      ref.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
    }
  }, [selected]);
  const label = s.id.length > 22 ? s.id.slice(0, 21) + "…" : s.id;
  const status = s.status;
  const dot =
    status === "passed"
      ? "bg-green-400"
      : status === "failed"
        ? "bg-red-400"
        : status === "running"
          ? "bg-indigo-400 animate-pulse"
          : status === "skipped"
            ? "bg-slate-400"
            : "bg-slate-600";
  let durMs = s.duration_ms ?? 0;
  if (!durMs && status === "running" && s.started_at) {
    durMs = Math.max(0, Date.now() - new Date(s.started_at).getTime());
  }
  return (
    <div
      ref={ref}
      className={`pl-9 pr-2 py-1 border-b border-[var(--border)]/60 cursor-pointer hover:bg-[var(--surface-raised)] transition-colors ${selected ? "bg-amber-400/10 border-l-2 border-l-amber-400" : ""}`}
      onClick={onClick}
      title={`${s.id}${status ? ` · ${status}` : ""}${durMs ? ` · ${fmtMs(durMs)}` : ""}`}
    >
      <div className="flex items-center gap-1.5">
        <span className={`w-1.5 h-1.5 rounded-full shrink-0 ${dot}`} />
        <span className="text-[10.5px] truncate flex-1 min-w-0 text-[var(--muted)]">
          {label}
        </span>
        {(s.annotations?.length ?? 0) > 0 && (
          <span
            className="text-[10px] font-mono text-cyan-300 shrink-0"
            title={`${s.annotations!.length} annotation${s.annotations!.length === 1 ? "" : "s"}`}
          >
            {s.annotations!.length}
          </span>
        )}
        {durMs > 0 && (
          <span className="text-[10px] font-mono text-[var(--muted)] shrink-0 tabular-nums">
            {fmtMs(durMs)}
          </span>
        )}
      </div>
    </div>
  );
}

// --- run row variants ---

const FullRunRow = memo(function FullRunRow({
  r,
  ctx,
  compact = false,
  progress,
}: {
  r: Run;
  ctx: FilterCtx;
  compact?: boolean;
  progress?: { done: number; total: number };
}) {
  if (compact) return <CompactFullRunRow r={r} ctx={ctx} progress={progress} />;
  const startedMs = new Date(r.started_at).getTime();
  const finishedMs = r.finished_at ? new Date(r.finished_at).getTime() : 0;
  const elapsedMs = (finishedMs || Date.now()) - startedMs;
  const sinceTs = r.finished_at || r.started_at;
  const repo = repoLabel(r);
  const sha7 = r.git_sha ? r.git_sha.slice(0, 7) : "";

  const meta = (
    <div className="min-w-0 flex flex-col gap-0.5 text-[11px]">
      <div className="flex items-center gap-2 min-w-0">
        <FilterableValue
          facet="status"
          value={r.status}
          ctx={ctx}
          tooltip={`Status: ${r.status}`}
        >
          <span
            className={`inline-block align-middle w-2.5 h-2.5 rounded-full shrink-0 ${statusDot(r.status)}`}
          />
        </FilterableValue>
        <FilterableValue
          facet="repo"
          value={repo}
          ctx={ctx}
          tooltip={`Repo: ${repo}`}
        >
          <span className="text-cyan-400/70 shrink-0">{repo}</span>
        </FilterableValue>
        <span className="text-[var(--muted)] shrink-0">/</span>
        <FilterableValue
          facet="pipeline"
          value={r.pipeline}
          ctx={ctx}
          tooltip={`Pipeline: ${r.pipeline}`}
        >
          <span className="font-medium text-violet-300 truncate">
            {r.pipeline}
          </span>
        </FilterableValue>
        {r.git_branch && (
          <FilterableValue
            facet="branch"
            value={r.git_branch}
            ctx={ctx}
            tooltip={`Branch: ${r.git_branch}`}
          >
            <span className="text-amber-400/70 shrink-0">
              ⎇ {truncate(r.git_branch, 40)}
            </span>
          </FilterableValue>
        )}
        {sha7 && (
          <FilterableValue
            facet="commit"
            value={sha7}
            ctx={ctx}
            tooltip={`Commit: ${sha7}`}
          >
            <span className="font-mono text-[var(--muted)] shrink-0">
              {sha7}
            </span>
          </FilterableValue>
        )}
        {r.trigger_source && (
          <Tooltip content={`Trigger: ${r.trigger_source}`}>
            <span className="font-mono text-[10px] text-[var(--muted)] shrink-0">
              {r.trigger_source}
            </span>
          </Tooltip>
        )}
      </div>
      <div className="flex items-center gap-1.5 font-mono tabular-nums text-[var(--muted)] min-w-0">
        {fmtDatePrefix(r.started_at) && (
          <span className="text-[var(--foreground)] shrink-0">
            {fmtDatePrefix(r.started_at)}
          </span>
        )}
        <FilterableTimestamp
          iso={r.started_at}
          field="started"
          ctx={ctx}
          tooltip={`Started ${fmtFullDate(r.started_at)}`}
        >
          <span className="text-[var(--foreground)] shrink-0">
            {fmtClock(r.started_at)}
          </span>
        </FilterableTimestamp>
        <span className="shrink-0">→</span>
        {r.finished_at ? (
          <>
            {fmtDatePrefix(r.finished_at) &&
              fmtDatePrefix(r.finished_at) !== fmtDatePrefix(r.started_at) && (
                <span className="text-[var(--foreground)] shrink-0">
                  {fmtDatePrefix(r.finished_at)}
                </span>
              )}
            <FilterableTimestamp
              iso={r.finished_at}
              field="finished"
              ctx={ctx}
              tooltip={`Finished ${fmtFullDate(r.finished_at!)}`}
            >
              <span className="text-[var(--foreground)] shrink-0">
                {fmtClock(r.finished_at)}
              </span>
            </FilterableTimestamp>
          </>
        ) : (
          <Tooltip content="Finished">
            <span className="text-[var(--foreground)] shrink-0">—</span>
          </Tooltip>
        )}
        {elapsedMs > 0 && (
          <Tooltip content="Duration">
            <span className="shrink-0">({fmtMs(elapsedMs)})</span>
          </Tooltip>
        )}
        <Tooltip content={fmtFullDate(sinceTs)}>
          <span className="shrink-0">· {fmtAgo(sinceTs)}</span>
        </Tooltip>
      </div>
    </div>
  );

  return (
    <div className="grid grid-cols-[minmax(20rem,40rem)_minmax(0,1fr)] gap-6 items-start">
      {meta}
      <div
        className="min-w-0 text-[11px] font-mono truncate"
        title={r.error || r.status}
      >
        {r.error ? (
          <span className="text-red-400">error: {r.error}</span>
        ) : (
          <span className="text-[var(--muted)]">{r.status}</span>
        )}
      </div>
    </div>
  );
});

const CompactFullRunRow = memo(function CompactFullRunRow({
  r,
  ctx,
  progress,
}: {
  r: Run;
  ctx: FilterCtx;
  progress?: { done: number; total: number };
}) {
  const startedMs = new Date(r.started_at).getTime();
  const finishedMs = r.finished_at ? new Date(r.finished_at).getTime() : 0;
  const elapsedMs = (finishedMs || Date.now()) - startedMs;
  const sinceTs = r.finished_at || r.started_at;
  const repo = repoLabel(r);
  const sha7 = r.git_sha ? r.git_sha.slice(0, 7) : "";

  const styleFor = (facet: FilterFacet, value: string) => {
    if (ctx.isIncluded(facet, value))
      return "underline decoration-dotted decoration-2 decoration-current underline-offset-4";
    if (ctx.isExcluded(facet, value))
      return "line-through decoration-red-400 opacity-70";
    return "";
  };

  const fullTitle = `${r.status.toUpperCase()} · ${repo}/${r.pipeline}${r.git_branch ? ` · ⎇ ${r.git_branch}` : ""}${sha7 ? ` · ${sha7}` : ""}${r.trigger_source ? ` · trigger: ${r.trigger_source}` : ""}\nStarted ${fmtFullDate(r.started_at)}${r.finished_at ? ` · Finished ${fmtFullDate(r.finished_at)}` : ""}`;
  const datePrefix = fmtDatePrefix(r.started_at);
  const [repoShort, pipelineShort, branchShort] = waterFill(
    [repo, r.pipeline, r.git_branch || ""],
    24,
  );
  return (
    <Tooltip
      content={
        <span className="whitespace-pre-wrap font-mono">{fullTitle}</span>
      }
    >
      <div className="min-w-0 flex flex-col gap-0.5 text-[11px]">
        <div className="flex items-center gap-1 min-w-0">
          <span
            className={`inline-block align-middle w-2.5 h-2.5 rounded-full shrink-0 ${statusDot(r.status)} ${styleFor("status", r.status)}`}
          />
          <span
            className={`text-cyan-400/70 shrink-0 ${styleFor("repo", repo)}`}
          >
            {repoShort}
          </span>
          <span className="text-[var(--muted)] shrink-0">/</span>
          <span
            className={`font-medium text-violet-300 shrink-0 ${styleFor("pipeline", r.pipeline)}`}
          >
            {pipelineShort}
          </span>
          {branchShort && (
            <>
              <span className="text-[var(--muted)] shrink-0">/</span>
              <span
                className={`text-amber-400/70 shrink-0 ${styleFor("branch", r.git_branch!)}`}
              >
                {branchShort}
              </span>
            </>
          )}
        </div>
        <div className="flex items-center gap-1.5 font-mono tabular-nums text-[var(--muted)] min-w-0">
          {r.trigger_source ? (
            <span className="text-[10px] text-[var(--muted)] shrink-0 w-2.5 text-center uppercase">
              {r.trigger_source.charAt(0)}
            </span>
          ) : (
            <span className="w-2.5 shrink-0" />
          )}
          {datePrefix && (
            <span className="text-[var(--foreground)] shrink-0">
              {datePrefix}
            </span>
          )}
          <span className="text-[var(--foreground)] shrink-0">
            {fmtClock(r.started_at)}
          </span>
          {elapsedMs > 0 && (
            <span className="shrink-0">({fmtMsCompact(elapsedMs)})</span>
          )}
          {progress && r.status === "running" && (
            <span className="shrink-0 text-indigo-300">
              {progress.done}/{progress.total}
            </span>
          )}
          <span className="shrink-0">{fmtAgoShort(sinceTs)}</span>
        </div>
      </div>
    </Tooltip>
  );
});

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

// --- detail pane ---

type TabKey = "logs" | "resources" | "dag" | "timeline" | "summary" | "setup";

interface TabDescriptor {
  key: TabKey;
  label: string;
  count?: string;
  visible: boolean;
}

function buildVisibleTabs(nodes: RunNode[]): TabDescriptor[] {
  return (
    [
      { key: "summary" as const, label: "Summary", visible: true },
      { key: "setup" as const, label: "Setup", visible: true },
      {
        key: "dag" as const,
        label: "DAG",
        count: nodes.length ? `${nodes.length}` : undefined,
        visible: nodes.length > 0,
      },
      { key: "logs" as const, label: "Logs", visible: true },
      {
        key: "timeline" as const,
        label: "Timeline",
        visible: nodes.length > 0,
      },
      { key: "resources" as const, label: "Resources", visible: true },
    ] as TabDescriptor[]
  ).filter((t) => t.visible);
}

function RunDetailPane({
  run,
  nodes,
  node,
  selectedStep,
  showTrigger,
  setShowTrigger,
  onSelectNode,
  onSelectStep,
  onRefresh,
  tab,
  setTab,
  focusedTab,
}: {
  run: Run;
  nodes: RunNode[];
  node: RunNode | null;
  selectedStep: string | null;
  showTrigger: boolean;
  setShowTrigger: (v: boolean) => void;
  onSelectNode: (id: string | null) => void;
  onSelectStep: (nodeId: string, stepId: string | null) => void;
  onRefresh: () => void;
  tab: TabKey;
  setTab: (k: TabKey) => void;
  focusedTab: TabKey | null;
}) {
  const selected = node;
  const selectedIsRunning =
    !!selected && !selected.finished_at && selected.status !== "pending";
  const runIsActive = run.status === "running";
  const isTerminal =
    run.status === "success" ||
    run.status === "failed" ||
    run.status === "cancelled";
  const visibleTabs = buildVisibleTabs(nodes);

  const selectedId = selected?.id ?? null;
  const tabContentRef = useRef<HTMLDivElement>(null);
  // Reset scroll position when switching tabs so the new tab opens
  // at the top, not wherever the previous tab was parked.
  useEffect(() => {
    tabContentRef.current?.scrollTo({ top: 0 });
  }, [tab]);
  const prevSelectedRef = useRef<string | null>(selectedId);

  // The previous-selection ref is kept so future routing decisions
  // could compare against it, but we intentionally do NOT auto-switch
  // the tab on selection changes — the user's tab choice persists
  // when flipping nodes or deselecting.
  useEffect(() => {
    prevSelectedRef.current = selectedId;
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
            data-tab-key={t.key}
            onClick={() => {
              if (effectiveTab === t.key) {
                tabContentRef.current?.scrollTo({ top: 0, behavior: "smooth" });
              } else {
                setTab(t.key);
              }
            }}
            className={`text-xs px-3 py-2 border-b-2 transition-colors -mb-px whitespace-nowrap rounded-t ${
              effectiveTab === t.key
                ? "border-cyan-400 text-[var(--foreground)]"
                : "border-transparent text-[var(--muted)] hover:text-[var(--foreground)]"
            } ${
              focusedTab === t.key
                ? "ring-2 ring-inset ring-cyan-300 bg-cyan-500/10"
                : ""
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

      <div
        ref={tabContentRef}
        className="flex-1 overflow-y-auto bg-[#0d1117] relative"
      >
        {effectiveTab === "logs" && (
          <div className="p-4">
            <LogsPane
              run={run}
              node={selected}
              nodes={nodes}
              focusStep={selectedStep}
              onSelectNode={onSelectNode}
            />
          </div>
        )}
        {effectiveTab === "resources" && (
          <div className="p-4">
            <AllNodesResources
              run={run}
              nodes={nodes}
              focusNode={selected?.id || null}
              onSelectNode={onSelectNode}
            />
          </div>
        )}
        {effectiveTab === "dag" && (
          <div className="p-4">
            <DAG
              nodes={nodes}
              selected={selected?.id || null}
              selectedStep={selectedStep}
              onSelect={onSelectNode}
              onSelectStep={onSelectStep}
              runId={run.id}
            />
          </div>
        )}
        {effectiveTab === "timeline" && (
          <div className="p-4">
            <ExecutionWaterfall
              run={run}
              nodes={nodes}
              focusNode={selected?.id || null}
              focusStep={selectedStep}
              onSelectNode={onSelectNode}
              onSelectStep={onSelectStep}
            />
          </div>
        )}
        {effectiveTab === "summary" && (
          <div className="flex flex-col gap-3 p-4">
            <SummaryPanel
              run={run}
              nodes={nodes}
              collapsed={false}
              onToggle={() => {}}
              inline
            />
            <RunAnnotationsList nodes={nodes} onSelectNode={onSelectNode} />
          </div>
        )}
        {effectiveTab === "setup" && (
          <div className="p-4">
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
          </div>
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
  onSelectNode: (id: string | null) => void;
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

function LogsPane({
  run,
  node,
  nodes,
  focusStep,
  onSelectNode,
}: {
  run: Run;
  node: RunNode | null;
  nodes?: RunNode[];
  focusStep?: string | null;
  onSelectNode?: (id: string) => void;
}) {
  return (
    <AllNodesLogs
      run={run}
      nodes={nodes || []}
      focusNode={node?.id || null}
      focusStep={focusStep ?? null}
      onSelectNode={onSelectNode}
    />
  );
}

// SingleNodeLogs renders the streaming/stored log body for one
// node, deciding by status. Used inside AllNodesLogs sections.
function SingleNodeLogs({
  run,
  node,
  focusStep,
}: {
  run: Run;
  node: RunNode;
  focusStep?: string | null;
}) {
  if (node.status === "pending") {
    return (
      <div className="text-sm text-[var(--muted)]">
        Node is pending -- waiting for dependencies.
      </div>
    );
  }
  const body =
    node.status === "approval_pending" || !node.finished_at ? (
      <StreamingLogs runID={run.id} nodeID={node.id} focusStep={focusStep} />
    ) : (
      <StoredLogs runID={run.id} nodeID={node.id} focusStep={focusStep} />
    );
  return (
    <div className="flex flex-col gap-2">
      <NodeLogSummary node={node} />
      {body}
    </div>
  );
}

// NodeLogSummary shows a one-or-two-line, glanceable block above the
// step list: outcome, failure reason / error message, exit code,
// duration. Hidden entirely when there's nothing useful to add
// beyond a plain "success".
function NodeLogSummary({ node }: { node: RunNode }) {
  const outcome = node.outcome || node.status;
  const isFailed =
    outcome === "failed" || node.status === "failed" || !!node.error;
  const isRunning = !node.finished_at && node.status !== "pending";
  const annotations = node.annotations ?? [];
  // Plain success with nothing to surface: hide the block entirely so
  // it doesn't add noise.
  if (
    !isFailed &&
    !isRunning &&
    !node.failure_reason &&
    !node.error &&
    annotations.length === 0
  ) {
    return null;
  }
  const tone = isFailed
    ? "border-red-500/40 bg-red-500/5"
    : isRunning
      ? "border-indigo-500/40 bg-indigo-500/5"
      : "border-[var(--border)] bg-[#161b22]";
  const labelTone = isFailed
    ? "text-red-300"
    : isRunning
      ? "text-indigo-300"
      : "text-[var(--muted)]";
  return (
    <div className={`border rounded-lg p-2 text-xs ${tone}`}>
      <div className="flex items-center gap-2 flex-wrap">
        <span
          className={`uppercase tracking-wider font-bold text-[10px] ${labelTone}`}
        >
          {outcome}
        </span>
        {node.failure_reason && (
          <span className="font-mono text-[10px] text-red-300/80">
            {node.failure_reason}
          </span>
        )}
        {typeof node.exit_code === "number" && node.exit_code !== 0 && (
          <span className="font-mono text-[10px] text-[var(--muted)]">
            exit {node.exit_code}
          </span>
        )}
        {node.duration_ms > 0 && (
          <span className="font-mono text-[10px] text-[var(--muted)] ml-auto">
            {fmtMs(node.duration_ms)}
          </span>
        )}
      </div>
      {node.error && (
        <div className="mt-1 font-mono text-[11px] text-red-300/90 whitespace-pre-wrap break-words">
          {node.error}
        </div>
      )}
      {!node.error && node.status_detail && (
        <div className="mt-1 font-mono text-[11px] text-[var(--muted)] whitespace-pre-wrap break-words">
          {node.status_detail}
        </div>
      )}
      {annotations.length > 0 && (
        <ul className="mt-2 flex flex-col gap-0.5">
          {annotations.map((a, i) => (
            <li
              key={i}
              className="font-mono text-[11px] text-[var(--foreground)] flex items-start gap-2"
            >
              <span className="text-[var(--muted)] shrink-0">›</span>
              <span className="whitespace-pre-wrap break-words">{a}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// RunAnnotationsList flattens every node's annotations into a single
// scannable list grouped by node, for the Summary tab when no node
// is selected. Clicking a node header filters the page to that node.
function RunAnnotationsList({
  nodes,
  onSelectNode,
}: {
  nodes: RunNode[];
  onSelectNode: (id: string | null) => void;
}) {
  const groups = nodes.filter((n) => (n.annotations?.length ?? 0) > 0);
  if (groups.length === 0) {
    return (
      <div className="text-xs text-[var(--muted)]">
        No annotations on this run. Steps can post one with{" "}
        <span className="font-mono">sparkwing.Annotate(ctx, msg)</span>.
      </div>
    );
  }
  const total = groups.reduce((n, g) => n + (g.annotations?.length ?? 0), 0);
  return (
    <div className="flex flex-col gap-2">
      <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
        Annotations ({total})
      </div>
      {groups.map((n) => (
        <div
          key={n.id}
          className="border border-[var(--border)] rounded p-2 bg-[#0d1117]"
        >
          <div className="flex items-center gap-2 mb-1">
            <span
              className={`w-2 h-2 rounded-full shrink-0 ${outcomeDot(n.outcome, n.status)}`}
            />
            <button
              onClick={() => onSelectNode(n.id)}
              className="font-mono text-xs text-[var(--accent)] hover:underline truncate"
              title={`select ${n.id}`}
            >
              {n.id}
            </button>
            <span className="text-[10px] font-mono text-[var(--muted)] ml-auto">
              {n.annotations!.length}
            </span>
          </div>
          <ul className="flex flex-col gap-0.5 pl-3.5">
            {n.annotations!.map((a, i) => (
              <li
                key={i}
                className="font-mono text-[11px] text-[var(--foreground)] flex items-start gap-2"
              >
                <span className="text-cyan-300 shrink-0">›</span>
                <span className="whitespace-pre-wrap break-words">{a}</span>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </div>
  );
}

// AllNodesLogs renders one collapsible block per node. Expanding a
// block lazy-mounts the existing single-node LogsPane underneath
// (StreamingLogs for live nodes, StoredLogs for finished ones, both
// of which use LogBucketView with step-level collapses inside).
function AllNodesLogs({
  run,
  nodes,
  focusNode,
  focusStep,
  onSelectNode,
}: {
  run: Run;
  nodes: RunNode[];
  focusNode?: string | null;
  focusStep?: string | null;
  onSelectNode?: (id: string) => void;
}) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const toggle = (id: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  // When a node selection arrives from outside, collapse other
  // sections so only the selected node is open, then scroll it in.
  useEffect(() => {
    if (!focusNode) return;
    setExpanded(new Set([focusNode]));
    requestAnimationFrame(() => {
      const el = document.querySelector(
        `[data-log-node-id="${focusNode}"]`,
      ) as HTMLElement | null;
      el?.scrollIntoView({ block: "start", behavior: "smooth" });
    });
  }, [focusNode]);
  if (nodes.length === 0) {
    return (
      <div className="text-sm text-[var(--muted)]">
        No nodes for this run yet.
      </div>
    );
  }
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center justify-between text-[10px] text-[var(--muted)] mb-1">
        <span>All nodes — expand a node to load its logs</span>
        <div className="flex items-center gap-2">
          <button
            onClick={() => setExpanded(new Set(nodes.map((n) => n.id)))}
            className="hover:text-[var(--foreground)] underline-offset-2 hover:underline"
          >
            expand all
          </button>
          <button
            onClick={() => setExpanded(new Set())}
            className="hover:text-[var(--foreground)] underline-offset-2 hover:underline"
          >
            collapse all
          </button>
        </div>
      </div>
      {nodes.map((n) => {
        const open = expanded.has(n.id);
        const dur = nodeDuration(n);
        const isFocus = focusNode === n.id;
        return (
          <div
            key={n.id}
            data-log-node-id={n.id}
            className={`border rounded bg-[#0d1117] ${isFocus ? "border-violet-400" : "border-[var(--border)]"}`}
          >
            <div
              onClick={() => toggle(n.id)}
              className="flex items-center gap-2 px-2 py-1.5 cursor-pointer hover:bg-[var(--surface-raised)] transition-colors"
            >
              <span className="text-[var(--muted)] w-3 text-center text-xs">
                {open ? "▾" : "▸"}
              </span>
              <span
                className={`w-2 h-2 rounded-full shrink-0 ${outcomeDot(n.outcome, n.status)}`}
              />
              {onSelectNode ? (
                <button
                  onClick={(e) => {
                    e.stopPropagation();
                    onSelectNode(n.id);
                  }}
                  className="font-mono text-xs text-left truncate max-w-[24rem] text-[var(--accent)] hover:underline"
                  title={`select ${n.id}`}
                >
                  {n.id}
                </button>
              ) : (
                <span
                  className="font-mono text-xs truncate max-w-[24rem]"
                  title={n.id}
                >
                  {n.id}
                </span>
              )}
              <span className="flex-1" />
              {(n.annotations?.length ?? 0) > 0 && (
                <span
                  className="text-[10px] font-mono text-cyan-300 shrink-0"
                  title={`${n.annotations!.length} annotation${n.annotations!.length === 1 ? "" : "s"}:\n${n.annotations!.join("\n")}`}
                >
                  › {n.annotations!.length}
                </span>
              )}
              <span className="text-[10px] font-mono text-[var(--muted)] shrink-0">
                {n.outcome || n.status}
              </span>
              {dur > 0 && (
                <span className="text-[10px] font-mono text-[var(--muted)] shrink-0">
                  {fmtMs(dur)}
                </span>
              )}
            </div>
            {open && (
              <div className="border-t border-[var(--border)] p-2">
                <SingleNodeLogs
                  run={run}
                  node={n}
                  focusStep={isFocus ? (focusStep ?? null) : null}
                />
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

// AllNodesResources renders one collapsible block per node with a
// ResourceChart inside. Selection auto-expands + scrolls just like
// AllNodesLogs; other sections collapse on selection.
function AllNodesResources({
  run,
  nodes,
  focusNode,
  onSelectNode,
}: {
  run: Run;
  nodes: RunNode[];
  focusNode?: string | null;
  onSelectNode?: (id: string) => void;
}) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const toggle = (id: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  useEffect(() => {
    if (!focusNode) return;
    setExpanded(new Set([focusNode]));
    requestAnimationFrame(() => {
      const el = document.querySelector(
        `[data-resource-node-id="${focusNode}"]`,
      ) as HTMLElement | null;
      el?.scrollIntoView({ block: "start", behavior: "smooth" });
    });
  }, [focusNode]);
  if (nodes.length === 0) {
    return (
      <div className="text-sm text-[var(--muted)]">
        No nodes for this run yet.
      </div>
    );
  }
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center justify-between text-[10px] text-[var(--muted)] mb-1">
        <span>All nodes — expand to load CPU / memory over time</span>
        <div className="flex items-center gap-2">
          <button
            onClick={() => setExpanded(new Set(nodes.map((n) => n.id)))}
            className="hover:text-[var(--foreground)] underline-offset-2 hover:underline"
          >
            expand all
          </button>
          <button
            onClick={() => setExpanded(new Set())}
            className="hover:text-[var(--foreground)] underline-offset-2 hover:underline"
          >
            collapse all
          </button>
        </div>
      </div>
      {nodes.map((n) => {
        const open = expanded.has(n.id);
        const dur = nodeDuration(n);
        const isFocus = focusNode === n.id;
        const isRunning = !n.finished_at && n.status !== "pending";
        return (
          <div
            key={n.id}
            data-resource-node-id={n.id}
            className={`border rounded bg-[#0d1117] ${isFocus ? "border-violet-400" : "border-[var(--border)]"}`}
          >
            <div
              onClick={() => toggle(n.id)}
              className="flex items-center gap-2 px-2 py-1.5 cursor-pointer hover:bg-[var(--surface-raised)] transition-colors"
            >
              <span className="text-[var(--muted)] w-3 text-center text-xs">
                {open ? "▾" : "▸"}
              </span>
              <span
                className={`w-2 h-2 rounded-full shrink-0 ${outcomeDot(n.outcome, n.status)}`}
              />
              {onSelectNode ? (
                <button
                  onClick={(e) => {
                    e.stopPropagation();
                    onSelectNode(n.id);
                  }}
                  className="font-mono text-xs text-left truncate max-w-[24rem] text-[var(--accent)] hover:underline"
                  title={`select ${n.id}`}
                >
                  {n.id}
                </button>
              ) : (
                <span
                  className="font-mono text-xs truncate max-w-[24rem]"
                  title={n.id}
                >
                  {n.id}
                </span>
              )}
              <span className="flex-1" />
              {(n.annotations?.length ?? 0) > 0 && (
                <span
                  className="text-[10px] font-mono text-cyan-300 shrink-0"
                  title={`${n.annotations!.length} annotation${n.annotations!.length === 1 ? "" : "s"}:\n${n.annotations!.join("\n")}`}
                >
                  › {n.annotations!.length}
                </span>
              )}
              <span className="text-[10px] font-mono text-[var(--muted)] shrink-0">
                {n.outcome || n.status}
              </span>
              {dur > 0 && (
                <span className="text-[10px] font-mono text-[var(--muted)] shrink-0">
                  {fmtMs(dur)}
                </span>
              )}
            </div>
            {open && (
              <div className="border-t border-[var(--border)] p-2">
                <ResourceChart
                  runID={run.id}
                  nodeID={n.id}
                  isRunning={isRunning}
                />
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

function StreamingLogs({
  runID,
  nodeID,
  focusStep,
}: {
  runID: string;
  nodeID: string;
  focusStep?: string | null;
}) {
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
      <LogBucketView
        parsed={parsed}
        jobId={`${runID}-${nodeID}`}
        focusStep={focusStep}
      />
      <div ref={endRef} />
    </>
  );
}

function StoredLogs({
  runID,
  nodeID,
  focusStep,
}: {
  runID: string;
  nodeID: string;
  focusStep?: string | null;
}) {
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
  return (
    <LogBucketView
      parsed={parsed}
      jobId={`${runID}-${nodeID}`}
      focusStep={focusStep}
    />
  );
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
  selectedStep,
  onSelect,
  onSelectStep,
  runId,
}: {
  nodes: RunNode[];
  selected: string | null;
  selectedStep: string | null;
  onSelect: (id: string | null) => void;
  onSelectStep: (nodeId: string, stepId: string | null) => void;
  runId?: string;
}) {
  // Auto-scroll the selected node into view when arriving with a
  // selection (e.g. switching to the DAG tab from elsewhere) or when
  // selection changes. The node's group is tagged with data-node-id
  // so a querySelector lookup finds it after layout.
  const dagRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!selected) return;
    requestAnimationFrame(() => {
      const el = dagRef.current?.querySelector(
        `[data-node-id="${selected}"]`,
      ) as SVGGElement | null;
      // "nearest" leaves the viewport alone when the node is already
      // visible; only scrolls the minimum needed to reveal it. Avoids
      // snapping the page on every click when the whole DAG fits.
      el?.scrollIntoView({
        behavior: "smooth",
        block: "nearest",
        inline: "nearest",
      });
    });
  }, [selected]);
  // Hover state for the floating tooltip overlay. Tracks which node
  // the pointer is currently over plus its viewport coords so we can
  // render a position:fixed card next to the cursor. The card waits
  // 500ms before appearing so a quick mouse-over doesn't flash a card
  // every time the cursor crosses the DAG.
  const [hover, setHover] = useState<{
    node: RunNode;
    x: number;
    y: number;
  } | null>(null);
  const hoverTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const cancelHover = () => {
    if (hoverTimer.current !== null) {
      clearTimeout(hoverTimer.current);
      hoverTimer.current = null;
    }
  };
  const scheduleHover = (n: RunNode, x: number, y: number) => {
    cancelHover();
    hoverTimer.current = setTimeout(() => {
      setHover({ node: n, x, y });
      hoverTimer.current = null;
    }, 500);
  };
  useEffect(() => () => cancelHover(), []);
  const [chipHover, setChipHover] = useState<{
    text: string;
    x: number;
    y: number;
  } | null>(null);
  // When a node with inner steps is selected, render its step DAG as
  // a stacked panel beneath the main DAG.
  const selectedNode = selected
    ? (nodes.find((n) => n.id === selected) ?? null)
    : null;
  const nodeW = 168;
  const nodeH = 38;
  const colGap = 64;
  const rowGap = 26;
  const padX = 12;
  const padY = 32;
  const nodeHeight = () => nodeH;

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
  // Within-column ordering follows declaration order, with groups
  // anchored to their first member's position. Each "cluster" (a
  // named group, or a singleton ungrouped node) takes the minimum
  // declaration index of its members in this column. Clusters sort
  // by that anchor; group members sort by declaration index within.
  //
  // Result: the column reads in the same sequence the user wrote in
  // their DSL (and that the left Nodes panel renders), but grouped
  // members are still adjacent so the frame overlay's bounding box
  // doesn't swallow an outsider.
  const nodeOrder = new Map(nodes.map((n, i) => [n.id, i]));
  const nodeClusterKey = (n: RunNode): string => n.groups?.[0] || `:${n.id}`;
  for (const col of columns) {
    if (!col) continue;
    const anchor = new Map<string, number>();
    for (const n of col) {
      const k = nodeClusterKey(n);
      const idx = nodeOrder.get(n.id) ?? 0;
      const cur = anchor.get(k);
      if (cur === undefined || idx < cur) anchor.set(k, idx);
    }
    col.sort((a, b) => {
      const ka = nodeClusterKey(a);
      const kb = nodeClusterKey(b);
      if (ka !== kb) {
        return (anchor.get(ka) ?? 0) - (anchor.get(kb) ?? 0);
      }
      return (nodeOrder.get(a.id) ?? 0) - (nodeOrder.get(b.id) ?? 0);
    });
  }

  // Per-column max widths so nodes size to their labels but still
  // line up vertically. ~7px per mono char at fontSize=11, plus the
  // status dot (24px), duration cell (~46px), zoom chip (22px),
  // pills, and edge padding. Cap at a generous max so a long step id
  // doesn't run the column off-screen.
  const charPxApprox = 7;
  // dot(24) + duration(46) + pad(16). Step-count + error pills hang
  // off the bottom edge now, so they don't claim inline width.
  const baseChrome = 24 + 46 + 16;
  const measureNodeW = (n: RunNode): number => {
    const w = Math.ceil(n.id.length * charPxApprox + baseChrome);
    return Math.max(140, Math.min(360, w));
  };
  const columnWidths: number[] = columns.map((col) =>
    col ? Math.max(...col.map(measureNodeW)) : 0,
  );
  const columnStartX: number[] = [];
  {
    let x = padX;
    columnWidths.forEach((w, i) => {
      columnStartX[i] = x;
      x += w + colGap;
    });
  }

  // Group frames extend groupFramePad below the last member and
  // groupFramePad + groupLabelOffset above the first (for the label
  // strip). Pre-reserve that space when a column transitions between
  // groups (or in/out of ungrouped). Nodes also carry bottom-edge
  // badges (SKIPPED / annotation / error) that hang ~7px below the
  // rect, so the reservation is layered on TOP of rowGap rather than
  // collapsing into it -- a max() would let the frame eat the badge.
  const groupFramePad = 8;
  const groupLabelOffset = 14;
  const primaryGroupOf = (n: RunNode): string => n.groups?.[0] || "";
  const pos = new Map<string, { x: number; y: number; w: number }>();
  const columnHeights: number[] = [];
  columns.forEach((col, ci) => {
    if (!col) return;
    let y = padY;
    const w = columnWidths[ci];
    let prevGroup: string | null = null;
    col.forEach((n, idx) => {
      const g = primaryGroupOf(n);
      if (idx > 0 && g !== prevGroup) {
        if (prevGroup) y += groupFramePad;
        if (g) y += groupFramePad + groupLabelOffset;
      }
      pos.set(n.id, { x: columnStartX[ci], y, w });
      y += nodeH + rowGap;
      prevGroup = g;
    });
    columnHeights[ci] = y;
  });

  const width =
    padX +
    columnWidths.reduce((acc, w) => acc + w, 0) +
    Math.max(0, columns.length - 1) * colGap +
    padX;
  const height = padY + Math.max(padY, ...columnHeights);

  const rawEdges: { src: string; dst: string; onFailure?: boolean }[] = [];
  for (const n of nodes) {
    for (const d of n.deps || []) {
      if (byID.has(d)) rawEdges.push({ src: d, dst: n.id });
    }
    if (n.on_failure_of && byID.has(n.on_failure_of)) {
      rawEdges.push({ src: n.on_failure_of, dst: n.id, onFailure: true });
    }
  }
  // Edge collapsing in two directions:
  //   1. src → group:   one source has ≥2 edges into the same dest
  //      group → draw one line into the group frame instead of N.
  //      Keeps fan-out patterns (build → publish-{linux,darwin,...})
  //      readable.
  //   2. group → dst:   ≥2 sources in one group all point at the same
  //      destination → draw one line out of the group frame.
  //      Symmetric optimization for fan-in patterns.
  type CollapsedEdge =
    | {
        kind: "node";
        src: string;
        dst: string;
        onFailure?: boolean;
      }
    | {
        kind: "to-group";
        src: string;
        groupName: string;
        sampleDstStatus: RunNode | undefined;
      }
    | {
        kind: "from-group";
        groupName: string;
        dst: string;
      };
  const dstGroupOf = (id: string): string | null => {
    const n = byID.get(id);
    return n?.groups?.[0] || null;
  };
  // Pass 1: collapse src → group.
  const pass1: CollapsedEdge[] = [];
  type Bucket = { dsts: string[] };
  const toGroupBuckets = new Map<string, Bucket>();
  for (const e of rawEdges) {
    if (e.onFailure) {
      pass1.push({ kind: "node", src: e.src, dst: e.dst, onFailure: true });
      continue;
    }
    const g = dstGroupOf(e.dst);
    const srcInSameGroup = g && byID.get(e.src)?.groups?.includes(g);
    if (!g || srcInSameGroup) {
      pass1.push({ kind: "node", src: e.src, dst: e.dst });
      continue;
    }
    const key = `${e.src}::${g}`;
    let b = toGroupBuckets.get(key);
    if (!b) {
      b = { dsts: [] };
      toGroupBuckets.set(key, b);
    }
    b.dsts.push(e.dst);
  }
  for (const [key, b] of toGroupBuckets) {
    const [src, group] = key.split("::");
    if (b.dsts.length === 1) {
      pass1.push({ kind: "node", src, dst: b.dsts[0] });
    } else {
      pass1.push({
        kind: "to-group",
        src,
        groupName: group,
        sampleDstStatus: byID.get(b.dsts[0]),
      });
    }
  }
  // Pass 2: collapse group → dst. Only inspects plain node edges
  // (failure stays 1:1 for readability; to-group is already collapsed
  // by direction-1 and doesn't participate here).
  const edges: CollapsedEdge[] = [];
  const fromGroupBuckets = new Map<string, string[]>();
  for (const e of pass1) {
    if (e.kind !== "node" || e.onFailure) {
      edges.push(e);
      continue;
    }
    const sg = byID.get(e.src)?.groups?.[0] || "";
    const dstInSameGroup = sg && byID.get(e.dst)?.groups?.includes(sg);
    if (!sg || dstInSameGroup) {
      edges.push(e);
      continue;
    }
    const key = `${sg}::${e.dst}`;
    const arr = fromGroupBuckets.get(key) ?? [];
    arr.push(e.src);
    fromGroupBuckets.set(key, arr);
  }
  for (const [key, srcs] of fromGroupBuckets) {
    const [group, dst] = key.split("::");
    if (srcs.length === 1) {
      edges.push({ kind: "node", src: srcs[0], dst });
    } else {
      edges.push({ kind: "from-group", groupName: group, dst });
    }
  }

  // Group frames: compute the bounding box around every node sharing
  // the same `.Group("name")` tag so we can draw a labelled dashed
  // container behind them. Rendered before edges/nodes so it sits
  // visually beneath the DAG's active elements. Single-member groups
  // still get a frame so the visual grouping matches the nodes list
  // on the left -- the (safety) header shouldn't look like a
  // different feature from the DAG container.
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
      if (p.x + p.w > maxX) maxX = p.x + p.w;
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
  const groupFrameByName = new Map(groupFrames.map((g) => [g.name, g]));

  const stackedStepNode =
    selectedNode && (selectedNode.work?.steps?.length ?? 0) > 0
      ? selectedNode
      : null;

  return (
    <div className="flex flex-col gap-2">
      <div
        ref={dagRef}
        className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-2 overflow-x-auto"
      >
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
                fill="rgba(56,189,248,0.05)"
                stroke="rgba(56,189,248,0.55)"
                strokeWidth={1.25}
                strokeDasharray="5 3"
              />
              <text
                x={g.x + 10}
                y={g.y + 12}
                fill="rgba(165,243,252,0.95)"
                fontSize={11}
                fontWeight="bold"
                fontFamily="ui-monospace, monospace"
              >
                {g.name}
              </text>
            </g>
          ))}
          {edges.map((e, i) => {
            let x1: number, y1: number;
            let x2: number, y2: number;
            let color: string;
            let dashed = false;
            // An edge is "touched" by the selection when either endpoint
            // is the selected node, or when a collapsed group endpoint
            // contains the selected node. Touched edges paint gold and
            // thicker so the selection's neighborhood pops out.
            let touched = false;
            const groupContainsSelected = (g: string): boolean =>
              !!selected && !!byID.get(selected)?.groups?.includes(g);
            if (e.kind === "to-group") {
              const a = pos.get(e.src);
              const frame = groupFrameByName.get(e.groupName);
              if (!a || !frame) return null;
              x1 = a.x + a.w;
              y1 = a.y + nodeH / 2;
              x2 = frame.x;
              y2 = frame.y + frame.h / 2;
              color = dagEdgeColor(e.sampleDstStatus);
              touched =
                e.src === selected || groupContainsSelected(e.groupName);
            } else if (e.kind === "from-group") {
              const frame = groupFrameByName.get(e.groupName);
              const b = pos.get(e.dst);
              if (!frame || !b) return null;
              x1 = frame.x + frame.w;
              y1 = frame.y + frame.h / 2;
              x2 = b.x;
              y2 = b.y + nodeH / 2;
              color = dagEdgeColor(byID.get(e.dst));
              touched =
                e.dst === selected || groupContainsSelected(e.groupName);
            } else {
              const a = pos.get(e.src);
              const b = pos.get(e.dst);
              if (!a || !b) return null;
              x1 = a.x + a.w;
              y1 = a.y + nodeH / 2;
              x2 = b.x;
              y2 = b.y + nodeH / 2;
              color = e.onFailure
                ? "rgba(248,113,113,0.55)"
                : dagEdgeColor(byID.get(e.dst));
              dashed = !!e.onFailure;
              touched = e.src === selected || e.dst === selected;
            }
            if (touched) color = "rgba(251,191,36,0.95)";
            const dx = Math.max(32, (x2 - x1) * 0.4);
            return (
              <path
                key={i}
                d={`M ${x1} ${y1} C ${x1 + dx} ${y1}, ${x2 - dx} ${y2}, ${x2} ${y2}`}
                fill="none"
                stroke={color}
                strokeWidth={touched ? 2.25 : 1.5}
                strokeDasharray={dashed ? "5 4" : undefined}
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
                data-node-id={n.id}
                transform={`translate(${p.x}, ${p.y})`}
                onMouseEnter={(e) => scheduleHover(n, e.clientX, e.clientY)}
                onMouseMove={(e) =>
                  setHover((prev) =>
                    prev && prev.node.id === n.id
                      ? { node: n, x: e.clientX, y: e.clientY }
                      : prev,
                  )
                }
                onMouseLeave={() => {
                  cancelHover();
                  setHover((prev) =>
                    prev && prev.node.id === n.id ? null : prev,
                  );
                }}
              >
                <rect
                  width={p.w}
                  height={nodeH}
                  rx={6}
                  ry={6}
                  fill={fill}
                  stroke={border}
                  strokeWidth={isSel ? 2 : 1}
                />
                <g
                  onClick={() => onSelect(isSel ? null : n.id)}
                  style={{ cursor: "pointer" }}
                >
                  <rect width={p.w} height={nodeH} fill="transparent" />
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
                    {n.id}
                  </text>
                </g>
                <text
                  x={p.w - 8}
                  y={nodeH / 2 + 4}
                  textAnchor="end"
                  fill="rgba(148,163,184,0.8)"
                  fontSize={10}
                  fontFamily="ui-monospace, monospace"
                >
                  {fmtMs(nodeDuration(n))}
                </text>
                {n.dynamic && <DynamicPill nodeW={p.w} />}
                {n.approval && <ApprovalPill n={n} nodeW={p.w} />}
                {n.outcome === "cached" && !n.approval && (
                  <CachedPill nodeW={p.w} />
                )}
                {(n.spawned_pipelines?.length ?? 0) > 0 &&
                  !n.dynamic &&
                  !n.approval &&
                  n.outcome !== "cached" && (
                    <CrossPipelinePill
                      nodeW={p.w}
                      pipelines={n.spawned_pipelines!}
                    />
                  )}
                {(n.annotations?.length ?? 0) > 0 &&
                  (() => {
                    const text = `${n.annotations!.length} annotation${n.annotations!.length === 1 ? "" : "s"}\n${n.annotations!.join("\n")}`;
                    return (
                      <NodeBadge
                        x={6}
                        y={nodeH - 6}
                        width={22}
                        label={`${n.annotations!.length}`}
                        fill="rgba(34,211,238,0.95)"
                        onMouseEnter={(e) =>
                          setChipHover({ text, x: e.clientX, y: e.clientY })
                        }
                        onMouseMove={(e) =>
                          setChipHover({ text, x: e.clientX, y: e.clientY })
                        }
                        onMouseLeave={() => setChipHover(null)}
                      />
                    );
                  })()}
                {(() => {
                  // Bottom-right stack. Step-count pill anchors to the
                  // right edge; the error chip (when present) sits one
                  // slot to its left. Anchoring the step count there
                  // keeps the in-rect duration text free of the chip
                  // and gives the eye a stable "X steps · Y duration"
                  // read on every node.
                  const stepCount = n.work?.steps?.length ?? 0;
                  const hasError =
                    !!n.error ||
                    !!n.failure_reason ||
                    (typeof n.exit_code === "number" && n.exit_code !== 0);
                  if (stepCount === 0 && !hasError) return null;
                  let cursor = p.w - 6;
                  const elems: React.ReactElement[] = [];
                  if (stepCount > 0) {
                    const label = `${stepCount}`;
                    const w = Math.max(20, 10 + label.length * 6);
                    cursor -= w;
                    const tip = `${stepCount} step${stepCount === 1 ? "" : "s"} · select node to view`;
                    elems.push(
                      <NodeBadge
                        key="step-count"
                        x={cursor}
                        y={nodeH - 6}
                        width={w}
                        label={label}
                        fill="rgba(148,163,184,0.95)"
                        cursor="pointer"
                        onClick={() => {
                          const isSel = selected === n.id;
                          onSelect(isSel ? null : n.id);
                        }}
                        onMouseEnter={(e) =>
                          setChipHover({
                            text: tip,
                            x: e.clientX,
                            y: e.clientY,
                          })
                        }
                        onMouseMove={(e) =>
                          setChipHover({
                            text: tip,
                            x: e.clientX,
                            y: e.clientY,
                          })
                        }
                        onMouseLeave={() => setChipHover(null)}
                      />,
                    );
                    cursor -= 4;
                  }
                  if (hasError) {
                    const w = 18;
                    cursor -= w;
                    const text =
                      n.error || n.failure_reason || `exit ${n.exit_code}`;
                    elems.push(
                      <NodeBadge
                        key="error"
                        x={cursor}
                        y={nodeH - 6}
                        width={w}
                        label="!"
                        fill="rgba(248,113,113,0.95)"
                        onMouseEnter={(e) =>
                          setChipHover({ text, x: e.clientX, y: e.clientY })
                        }
                        onMouseMove={(e) =>
                          setChipHover({ text, x: e.clientX, y: e.clientY })
                        }
                        onMouseLeave={() => setChipHover(null)}
                      />,
                    );
                  }
                  return <>{elems}</>;
                })()}
                {n.outcome === "skipped" && (
                  <NodeBadge
                    x={p.w / 2 - 26}
                    y={nodeH - 6}
                    width={52}
                    label="SKIPPED"
                    fill="rgba(148,163,184,0.95)"
                    title="skipped"
                  />
                )}
              </g>
            );
          })}
        </svg>
        {hover && <DagNodeTooltip node={hover.node} x={hover.x} y={hover.y} />}
        {chipHover &&
          (() => {
            const w = typeof window === "undefined" ? 1920 : window.innerWidth;
            const alignRight = chipHover.x > w - 360;
            const style: React.CSSProperties = {
              position: "fixed",
              top: chipHover.y + 14,
              zIndex: 100,
              pointerEvents: "none",
              maxWidth: "min(90vw, 360px)",
              whiteSpace: "pre-wrap",
            };
            if (alignRight) style.right = w - chipHover.x + 14;
            else style.left = chipHover.x + 14;
            return (
              <div
                style={style}
                className="bg-[#1e293b] border border-[var(--border)] rounded px-2 py-1 text-[10px] font-mono text-[var(--foreground)] shadow-lg break-words"
              >
                {chipHover.text}
              </div>
            );
          })()}
      </div>
      {stackedStepNode && (
        <StepDag
          node={stackedStepNode}
          nodeW={nodeW}
          nodeH={nodeH}
          colGap={colGap}
          rowGap={rowGap}
          padX={padX}
          padY={padY}
          selectedStep={selectedStep}
          onSelectStep={(stepId) => onSelectStep(stackedStepNode.id, stepId)}
        />
      )}
    </div>
  );
}

// DagNodeTooltip is the floating info card shown on DAG-node hover.
// Rendered as a position:fixed sibling of the SVG so it escapes the
// SVG coordinate system and tracks the viewport cursor cleanly.
// Offset 14px down-right of the cursor so it doesn't sit under the
// mouse. Right-anchors when near the viewport edge so the card
// doesn't clip off-screen on rightmost-column hovers.
// StepDag is the zoomed-in view: the work.steps of one parent node
// rendered as a full-size DAG using the same dims as the outer
// graph. The header carries a breadcrumb back to the run-level view.
// stepColorFor hashes the step id into a stable palette pick so
// neighboring steps don't look like one big block. Two-tone (low-
// alpha fill + saturated stroke) keeps the inner step DAG legible
// against the dark canvas.
function stepColorFor(id: string): { fill: string; stroke: string } {
  const palette = [
    { fill: "rgba(56,189,248,0.18)", stroke: "rgba(56,189,248,0.9)" }, // cyan
    { fill: "rgba(167,139,250,0.18)", stroke: "rgba(167,139,250,0.9)" }, // violet
    { fill: "rgba(244,114,182,0.18)", stroke: "rgba(244,114,182,0.9)" }, // pink
    { fill: "rgba(34,197,94,0.18)", stroke: "rgba(34,197,94,0.9)" }, // green
    { fill: "rgba(251,191,36,0.18)", stroke: "rgba(251,191,36,0.9)" }, // amber
    { fill: "rgba(96,165,250,0.18)", stroke: "rgba(96,165,250,0.9)" }, // blue
    { fill: "rgba(248,113,113,0.18)", stroke: "rgba(248,113,113,0.9)" }, // red
  ];
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) | 0;
  return palette[Math.abs(h) % palette.length];
}

// Step rect coloring keyed by runtime status. Mirrors dagNodeColors:
// skipped is the lightest, pending the dim default, failed/passed
// use their dedicated hues. No "cancelled" state at the step layer.
function stepStatusColors(
  status?: "passed" | "failed" | "running" | "skipped",
): {
  fill: string;
  border: string;
} {
  switch (status) {
    case "passed":
      return { fill: "rgba(34,197,94,0.10)", border: "rgba(74,222,128,0.45)" };
    case "failed":
      return { fill: "rgba(239,68,68,0.12)", border: "rgba(248,113,113,0.55)" };
    case "running":
      return {
        fill: "rgba(99,102,241,0.12)",
        border: "rgba(129,140,248,0.55)",
      };
    case "skipped":
      return {
        fill: "rgba(148,163,184,0.04)",
        border: "rgba(148,163,184,0.25)",
      };
    default:
      // pending (no step_start yet)
      return {
        fill: "rgba(100,116,139,0.08)",
        border: "rgba(100,116,139,0.30)",
      };
  }
}

function StepDag({
  node,
  nodeW,
  nodeH,
  colGap,
  rowGap,
  padX,
  padY,
  onBack,
  selectedStep,
  onSelectStep,
}: {
  node: RunNode;
  nodeW: number;
  nodeH: number;
  colGap: number;
  rowGap: number;
  padX: number;
  padY: number;
  onBack?: () => void;
  selectedStep?: string | null;
  onSelectStep?: (stepId: string | null) => void;
}) {
  // Auto-scroll selected step into view (mirrors the run-level DAG).
  // "nearest" so we don't snap when the step is already visible.
  const stepDagRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!selectedStep) return;
    requestAnimationFrame(() => {
      const el = stepDagRef.current?.querySelector(
        `[data-step-id="${selectedStep}"]`,
      ) as SVGGElement | null;
      el?.scrollIntoView({
        behavior: "smooth",
        block: "nearest",
        inline: "nearest",
      });
    });
  }, [selectedStep]);
  // Hover state for the floating tooltip. Mirrors the run-level DAG:
  // 500ms delay before showing so a quick mouse-over doesn't flash.
  const [hover, setHover] = useState<{
    step: NodeWorkStep;
    x: number;
    y: number;
  } | null>(null);
  const hoverTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const cancelStepHover = () => {
    if (hoverTimer.current !== null) {
      clearTimeout(hoverTimer.current);
      hoverTimer.current = null;
    }
  };
  const scheduleStepHover = (s: NodeWorkStep, x: number, y: number) => {
    cancelStepHover();
    hoverTimer.current = setTimeout(() => {
      setHover({ step: s, x, y });
      hoverTimer.current = null;
    }, 500);
  };
  useEffect(() => () => cancelStepHover(), []);
  const steps = node.work?.steps ?? [];
  const byId = new Map(steps.map((s) => [s.id, s]));
  const level = new Map<string, number>();
  const resolve = (id: string): number => {
    const cached = level.get(id);
    if (cached !== undefined) return cached;
    const s = byId.get(id);
    if (!s || !s.needs || s.needs.length === 0) {
      level.set(id, 0);
      return 0;
    }
    level.set(id, 0);
    let l = 0;
    for (const d of s.needs) if (byId.has(d)) l = Math.max(l, resolve(d) + 1);
    level.set(id, l);
    return l;
  };
  for (const s of steps) resolve(s.id);
  // Map step id → its first named group (so column sort + frame
  // computation match the run-level DAG's per-row clustering).
  const stepGroupOf = new Map<string, string>();
  const stepGroups = node.work?.step_groups ?? [];
  for (const g of stepGroups) {
    if (!g.name) continue;
    for (const m of g.members) {
      if (!stepGroupOf.has(m)) stepGroupOf.set(m, g.name);
    }
  }
  const cols: NodeWorkStep[][] = [];
  for (const s of steps) {
    const l = level.get(s.id) ?? 0;
    if (!cols[l]) cols[l] = [];
    cols[l].push(s);
  }
  // Mirror the run-level DAG: each column orders by declaration
  // index with groups anchored to their first member. Keeps grouped
  // members adjacent (needed by the frame overlay) while letting the
  // overall column flow read the same as the left Nodes panel.
  const stepOrder = new Map(steps.map((s, i) => [s.id, i]));
  const stepClusterKey = (s: NodeWorkStep): string =>
    stepGroupOf.get(s.id) || `:${s.id}`;
  for (const col of cols) {
    if (!col) continue;
    const anchor = new Map<string, number>();
    for (const s of col) {
      const k = stepClusterKey(s);
      const idx = stepOrder.get(s.id) ?? 0;
      const cur = anchor.get(k);
      if (cur === undefined || idx < cur) anchor.set(k, idx);
    }
    col.sort((a, b) => {
      const ka = stepClusterKey(a);
      const kb = stepClusterKey(b);
      if (ka !== kb) {
        return (anchor.get(ka) ?? 0) - (anchor.get(kb) ?? 0);
      }
      return (stepOrder.get(a.id) ?? 0) - (stepOrder.get(b.id) ?? 0);
    });
  }
  // Mirror the run-level DAG spacing: reserve frame-label space and
  // bottom padding on top of rowGap when crossing a group boundary,
  // so a frame doesn't bleed into the row above or below.
  const groupFramePad = 8;
  const groupLabelOffset = 14;
  const pos = new Map<string, { x: number; y: number }>();
  const colMaxY: number[] = [];
  cols.forEach((col, ci) => {
    if (!col) return;
    let y = padY;
    let prevGroup: string | null = null;
    col.forEach((s, idx) => {
      const g = stepGroupOf.get(s.id) || "";
      if (idx > 0 && g !== prevGroup) {
        if (prevGroup) y += groupFramePad;
        if (g) y += groupFramePad + groupLabelOffset;
      }
      pos.set(s.id, {
        x: padX + ci * (nodeW + colGap),
        y,
      });
      y += nodeH + rowGap;
      prevGroup = g;
    });
    colMaxY[ci] = y;
  });
  const width =
    padX * 2 +
    Math.max(1, cols.length) * nodeW +
    Math.max(0, cols.length - 1) * colGap;
  const height = padY + Math.max(padY, ...colMaxY);

  // Step-group frames: bounding box around each group's members.
  const stepGroupFrames: {
    name: string;
    accent: string;
    x: number;
    y: number;
    w: number;
    h: number;
  }[] = [];
  for (const g of stepGroups) {
    if (!g.name || g.members.length === 0) continue;
    let minX = Infinity,
      minY = Infinity,
      maxX = -Infinity,
      maxY = -Infinity;
    for (const m of g.members) {
      const p = pos.get(m);
      if (!p) continue;
      if (p.x < minX) minX = p.x;
      if (p.y < minY) minY = p.y;
      if (p.x + nodeW > maxX) maxX = p.x + nodeW;
      if (p.y + nodeH > maxY) maxY = p.y + nodeH;
    }
    if (!isFinite(minX)) continue;
    stepGroupFrames.push({
      name: g.name,
      accent: stepColorFor(g.name).stroke,
      x: minX - groupFramePad,
      y: minY - groupFramePad - groupLabelOffset,
      w: maxX - minX + groupFramePad * 2,
      h: maxY - minY + groupFramePad * 2 + groupLabelOffset,
    });
  }
  const stepGroupFrameByName = new Map(stepGroupFrames.map((f) => [f.name, f]));
  // Collapse step edges in both directions:
  //   1. step → group:  one source with ≥2 needs in the same group →
  //      one line into the group frame.
  //   2. group → step:  ≥2 sources in one group all feed the same
  //      destination → one line out of the group frame.
  type StepEdge =
    | { kind: "step"; src: string; dst: string }
    | { kind: "to-group"; src: string; groupName: string }
    | { kind: "from-group"; groupName: string; dst: string };
  const rawStepEdges: { src: string; dst: string }[] = [];
  for (const s of steps) {
    for (const d of s.needs ?? []) {
      if (byId.has(d)) rawStepEdges.push({ src: d, dst: s.id });
    }
  }
  const pass1Step: StepEdge[] = [];
  const toGroupStepBuckets = new Map<string, string[]>();
  for (const e of rawStepEdges) {
    const g = stepGroupOf.get(e.dst);
    const srcGroup = stepGroupOf.get(e.src);
    if (!g || srcGroup === g) {
      pass1Step.push({ kind: "step", src: e.src, dst: e.dst });
      continue;
    }
    const key = `${e.src}::${g}`;
    const arr = toGroupStepBuckets.get(key) ?? [];
    arr.push(e.dst);
    toGroupStepBuckets.set(key, arr);
  }
  for (const [key, dsts] of toGroupStepBuckets) {
    const [src, group] = key.split("::");
    if (dsts.length === 1) {
      pass1Step.push({ kind: "step", src, dst: dsts[0] });
    } else {
      pass1Step.push({ kind: "to-group", src, groupName: group });
    }
  }
  const stepEdges: StepEdge[] = [];
  const fromGroupStepBuckets = new Map<string, string[]>();
  for (const e of pass1Step) {
    if (e.kind !== "step") {
      stepEdges.push(e);
      continue;
    }
    const sg = stepGroupOf.get(e.src);
    const dg = stepGroupOf.get(e.dst);
    if (!sg || sg === dg) {
      stepEdges.push(e);
      continue;
    }
    const key = `${sg}::${e.dst}`;
    const arr = fromGroupStepBuckets.get(key) ?? [];
    arr.push(e.src);
    fromGroupStepBuckets.set(key, arr);
  }
  for (const [key, srcs] of fromGroupStepBuckets) {
    const [group, dst] = key.split("::");
    if (srcs.length === 1) {
      stepEdges.push({ kind: "step", src: srcs[0], dst });
    } else {
      stepEdges.push({ kind: "from-group", groupName: group, dst });
    }
  }

  return (
    <div
      ref={stepDagRef}
      className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-2 overflow-x-auto"
    >
      <div className="flex items-center gap-2 px-1 pb-2 text-xs">
        {onBack && (
          <button
            onClick={onBack}
            className="px-2 py-1 rounded border border-[var(--border)] text-[var(--muted)] hover:text-[var(--foreground)] hover:border-[var(--foreground)] transition-colors"
          >
            ← back to run
          </button>
        )}
        <span className="font-mono text-violet-300">{node.id}</span>
        <span className="text-[var(--muted)]">/</span>
        <span className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
          Steps
        </span>
        <span className="text-[var(--muted)]">
          ({steps.length} step{steps.length === 1 ? "" : "s"})
        </span>
      </div>
      {(node.annotations?.length ?? 0) > 0 && (
        <div className="mb-2 border border-[var(--border)] rounded p-2 bg-[#0d1117]">
          <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1">
            Annotations ({node.annotations!.length})
          </div>
          <ul className="flex flex-col gap-0.5">
            {node.annotations!.map((a, i) => (
              <li
                key={i}
                className="font-mono text-[11px] text-[var(--foreground)] flex items-start gap-2"
              >
                <span className="text-cyan-300 shrink-0">›</span>
                <span className="whitespace-pre-wrap break-words">{a}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
      {steps.length === 0 ? (
        <div className="px-1 py-4 text-sm text-[var(--muted)]">
          This node has no inner steps.
        </div>
      ) : (
        <svg
          width={width}
          height={height}
          style={{ minWidth: width, display: "block" }}
        >
          {stepGroupFrames.map((g) => (
            <g key={`stepgroup-${g.name}`}>
              <rect
                x={g.x}
                y={g.y}
                width={g.w}
                height={g.h}
                rx={8}
                ry={8}
                fill="rgba(56,189,248,0.05)"
                stroke={g.accent}
                strokeWidth={1.25}
                strokeDasharray="5 3"
              />
              <text
                x={g.x + 10}
                y={g.y + 12}
                fill="rgba(165,243,252,0.95)"
                fontSize={11}
                fontWeight="bold"
                fontFamily="ui-monospace, monospace"
              >
                {g.name}
              </text>
            </g>
          ))}
          {stepEdges.map((e, i) => {
            let x1: number, y1: number, x2: number, y2: number;
            // Mirror the run-level DAG: any edge connected to the
            // selected step (or to a group whose members include it)
            // paints gold so the in/out neighborhood pops out.
            let touched = false;
            const stepInGroup = (g: string): boolean =>
              !!selectedStep && stepGroupOf.get(selectedStep) === g;
            if (e.kind === "to-group") {
              const a = pos.get(e.src);
              const frame = stepGroupFrameByName.get(e.groupName);
              if (!a || !frame) return null;
              x1 = a.x + nodeW;
              y1 = a.y + nodeH / 2;
              x2 = frame.x;
              y2 = frame.y + frame.h / 2;
              touched = e.src === selectedStep || stepInGroup(e.groupName);
            } else if (e.kind === "from-group") {
              const frame = stepGroupFrameByName.get(e.groupName);
              const b = pos.get(e.dst);
              if (!frame || !b) return null;
              x1 = frame.x + frame.w;
              y1 = frame.y + frame.h / 2;
              x2 = b.x;
              y2 = b.y + nodeH / 2;
              touched = e.dst === selectedStep || stepInGroup(e.groupName);
            } else {
              const a = pos.get(e.src);
              const b = pos.get(e.dst);
              if (!a || !b) return null;
              x1 = a.x + nodeW;
              y1 = a.y + nodeH / 2;
              x2 = b.x;
              y2 = b.y + nodeH / 2;
              touched = e.src === selectedStep || e.dst === selectedStep;
            }
            const dx = Math.max(16, Math.abs(x2 - x1) / 2);
            return (
              <path
                key={i}
                d={`M ${x1} ${y1} C ${x1 + dx} ${y1}, ${x2 - dx} ${y2}, ${x2} ${y2}`}
                fill="none"
                stroke={
                  touched ? "rgba(251,191,36,0.95)" : "rgba(148,163,184,0.55)"
                }
                strokeWidth={touched ? 2.25 : 1.25}
              />
            );
          })}
          {steps.map((s) => {
            const p = pos.get(s.id);
            if (!p) return null;
            const status = s.status;
            const { fill, border } = stepStatusColors(status);
            const isSel = selectedStep === s.id;
            const dotClass =
              status === "failed"
                ? "fill-red-400"
                : status === "running"
                  ? "fill-indigo-400 animate-pulse"
                  : status === "passed"
                    ? "fill-green-400"
                    : status === "skipped"
                      ? "fill-slate-400"
                      : "fill-slate-600";
            return (
              <g
                key={s.id}
                data-step-id={s.id}
                transform={`translate(${p.x}, ${p.y})`}
                onClick={() => onSelectStep?.(isSel ? null : s.id)}
                onMouseEnter={(e) => scheduleStepHover(s, e.clientX, e.clientY)}
                onMouseMove={(e) =>
                  setHover((prev) =>
                    prev && prev.step.id === s.id
                      ? { step: s, x: e.clientX, y: e.clientY }
                      : prev,
                  )
                }
                onMouseLeave={() => {
                  cancelStepHover();
                  setHover((prev) =>
                    prev && prev.step.id === s.id ? null : prev,
                  );
                }}
                style={{ cursor: onSelectStep ? "pointer" : undefined }}
              >
                <rect
                  width={nodeW}
                  height={nodeH}
                  rx={6}
                  ry={6}
                  fill={fill}
                  stroke={isSel ? "rgba(251,191,36,0.95)" : border}
                  strokeWidth={isSel ? 2 : 1.5}
                />
                <circle cx={14} cy={nodeH / 2} r={4} className={dotClass} />
                <text
                  x={26}
                  y={nodeH / 2 + 4}
                  fill="currentColor"
                  fontSize={11}
                  fontFamily="ui-monospace, monospace"
                >
                  {truncate(s.id, 18)}
                </text>
                {(() => {
                  let ms = s.duration_ms ?? 0;
                  if (!ms && status === "running" && s.started_at) {
                    ms = Math.max(
                      0,
                      Date.now() - new Date(s.started_at).getTime(),
                    );
                  }
                  if (!ms) return null;
                  return (
                    <text
                      x={nodeW - 8}
                      y={nodeH / 2 + 4}
                      textAnchor="end"
                      fill="rgba(148,163,184,0.85)"
                      fontSize={10}
                      fontFamily="ui-monospace, monospace"
                    >
                      {fmtMs(ms)}
                    </text>
                  );
                })()}
                {(() => {
                  // Edge badges, stacked along the top edge. Result
                  // pill sits rightmost; skipIf to its left when both
                  // are present.
                  const badges: {
                    label: string;
                    fill: string;
                  }[] = [];
                  if (s.is_result)
                    badges.push({
                      label: "★ result",
                      fill: "rgba(74,222,128,0.95)",
                    });
                  if (s.has_skip_if)
                    badges.push({
                      label: "skipIf",
                      fill: "rgba(251,191,36,0.95)",
                    });
                  let rightEdge = nodeW - 6;
                  return badges.map((b, bi) => {
                    const w = 10 + b.label.length * 6;
                    rightEdge -= w + (bi > 0 ? 4 : 0);
                    return (
                      <NodeBadge
                        key={b.label}
                        x={rightEdge}
                        y={-7}
                        width={w}
                        label={b.label}
                        fill={b.fill}
                      />
                    );
                  });
                })()}
                {(s.annotations?.length ?? 0) > 0 &&
                  (() => {
                    const count = s.annotations!.length;
                    const title = `${count} annotation${count === 1 ? "" : "s"}\n${s.annotations!.join("\n")}`;
                    const label = `${count}`;
                    const w = Math.max(22, 10 + label.length * 6);
                    return (
                      <NodeBadge
                        x={6}
                        y={nodeH - 7}
                        width={w}
                        label={label}
                        fill="rgba(34,211,238,0.95)"
                        title={title}
                      />
                    );
                  })()}
              </g>
            );
          })}
        </svg>
      )}
      {hover && <StepTooltip step={hover.step} x={hover.x} y={hover.y} />}
    </div>
  );
}

function StepTooltip({
  step,
  x,
  y,
}: {
  step: NodeWorkStep;
  x: number;
  y: number;
}) {
  const status = step.status || "pending";
  const dot =
    status === "passed"
      ? "bg-green-400"
      : status === "failed"
        ? "bg-red-400"
        : status === "running"
          ? "bg-indigo-400 animate-pulse"
          : status === "skipped"
            ? "bg-slate-400"
            : "bg-slate-600";
  let dur = step.duration_ms ?? 0;
  if (!dur && status === "running" && step.started_at) {
    dur = Math.max(0, Date.now() - new Date(step.started_at).getTime());
  }
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
  if (step.is_result)
    badges.push({ label: "result", cls: "bg-green-500/20 text-green-300" });
  if (step.has_skip_if)
    badges.push({ label: "skipIf", cls: "bg-amber-500/20 text-amber-300" });
  return (
    <div style={style}>
      <div className="bg-[#1e293b] border border-[var(--border)] rounded-lg px-3 py-2 text-xs shadow-xl min-w-[220px] max-w-sm">
        <div className="flex items-center gap-2 mb-1">
          <span className={`w-2 h-2 rounded-full shrink-0 ${dot}`} />
          <span className="font-mono font-bold">{step.id}</span>
        </div>
        <div className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-[11px]">
          <span className="text-[var(--muted)]">State:</span>
          <span className="font-mono">{status}</span>
          {dur > 0 && (
            <>
              <span className="text-[var(--muted)]">Duration:</span>
              <span className="font-mono">{fmtMs(dur)}</span>
            </>
          )}
          {step.needs && step.needs.length > 0 && (
            <>
              <span className="text-[var(--muted)]">Needs:</span>
              <span className="font-mono">{step.needs.join(", ")}</span>
            </>
          )}
          {(step.annotations?.length ?? 0) > 0 && (
            <>
              <span className="text-[var(--muted)]">Annotations:</span>
              <span className="font-mono whitespace-pre-wrap">
                {step.annotations!.join("\n")}
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
          {(node.spawned_pipelines?.length ?? 0) > 0 && (
            <>
              <span className="text-[var(--muted)]">Spawns:</span>
              <span className="font-mono text-sky-300 whitespace-pre-wrap">
                {node
                  .spawned_pipelines!.map(
                    (p) => `↗ ${p.pipeline} (${p.child_run_id.slice(-8)})`,
                  )
                  .join("\n")}
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

// NodeBadge is the shared pill primitive every node-attached chip
// renders through (SKIPPED / annotation count / error indicator /
// step ★result + skipIf flags). Pill-shaped (rx = h/2), opaque fill,
// sans-serif label -- matches the corner-pill family (DynamicPill /
// ApprovalPill / CachedPill) so the whole node visual reads as one
// design system instead of two eras of ad-hoc inline SVG.
function NodeBadge({
  x,
  y,
  width,
  label,
  fill,
  fg = "rgba(15,15,15,0.95)",
  title,
  cursor,
  onMouseEnter,
  onMouseMove,
  onMouseLeave,
  onClick,
}: {
  x: number;
  y: number;
  width: number;
  label: string;
  fill: string;
  fg?: string;
  title?: string;
  cursor?: "pointer";
  onMouseEnter?: React.MouseEventHandler<SVGGElement>;
  onMouseMove?: React.MouseEventHandler<SVGGElement>;
  onMouseLeave?: React.MouseEventHandler<SVGGElement>;
  onClick?: React.MouseEventHandler<SVGGElement>;
}) {
  const h = 15;
  return (
    <g
      onMouseEnter={onMouseEnter}
      onMouseMove={onMouseMove}
      onMouseLeave={onMouseLeave}
      onClick={onClick}
      style={cursor ? { cursor } : undefined}
    >
      {title ? <title>{title}</title> : null}
      <rect
        x={x}
        y={y}
        width={width}
        height={h}
        rx={h / 2}
        ry={h / 2}
        fill={fill}
      />
      <text
        x={x + width / 2}
        y={y + h / 2 + 3.5}
        textAnchor="middle"
        fill={fg}
        fontSize={10}
        fontWeight={700}
        fontFamily="ui-sans-serif, system-ui, sans-serif"
        style={{ letterSpacing: "0.4px" }}
      >
        {label}
      </text>
    </g>
  );
}

// DynamicPill is the rainbow-gradient "DYNAMIC" corner badge painted
// on a DAG node whose shape is runtime-variable. Centered along the
// top edge of the node, overhanging upward -- keeps the pill from
// clipping the left or right SVG boundary regardless of column.
function DynamicPill({ nodeW }: { nodeW: number }) {
  const pillW = 56;
  const pillH = 15;
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
        y={y + pillH / 2 + 3.5}
        textAnchor="middle"
        fill="white"
        fontSize={10}
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
  const pillH = 15;
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
        y={y + pillH / 2 + 3.5}
        textAnchor="middle"
        fill="rgba(15,15,15,0.95)"
        fontSize={10}
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
  const pillH = 15;
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
        y={y + pillH / 2 + 3.5}
        textAnchor="middle"
        fill="rgba(20,15,30,0.95)"
        fontSize={10}
        fontWeight={700}
        fontFamily="ui-sans-serif, system-ui, sans-serif"
        style={{ letterSpacing: "0.5px" }}
      >
        CACHED
      </text>
    </g>
  );
}

// CrossPipelinePill marks a node that fired sparkwing.RunAndAwait
// during its body. Sky-cyan to read as "outgoing connection," with
// the target pipeline name when only one child was spawned, or a
// `↗ N pipelines` count when there were several. The label is the
// node's tooltip-side context too — operators reading the hover
// card see the full list of child run ids.
function CrossPipelinePill({
  nodeW,
  pipelines,
}: {
  nodeW: number;
  pipelines: SpawnedPipelineRef[];
}) {
  const label =
    pipelines.length === 1
      ? `↗ ${truncate(pipelines[0].pipeline, 16)}`
      : `↗ ${pipelines.length} pipelines`;
  const pillH = 15;
  const pillW = Math.max(48, 12 + label.length * 6);
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
        fill="rgba(56,189,248,0.95)"
      />
      <text
        x={x + pillW / 2}
        y={y + pillH / 2 + 3.5}
        textAnchor="middle"
        fill="rgba(8,15,30,0.95)"
        fontSize={10}
        fontWeight={700}
        fontFamily="ui-sans-serif, system-ui, sans-serif"
        style={{ letterSpacing: "0.3px" }}
      >
        {label}
      </text>
    </g>
  );
}

function truncate(s: string, n: number): string {
  return s.length <= n ? s : s.slice(0, n - 1) + "…";
}

// waterFill distributes a total character budget across `items` so
// short strings stay intact and the slack goes to longer ones. Each
// returned string is the original truncated with an ellipsis when it
// got squeezed, or untouched when it fit within its share.
function waterFill(items: string[], total: number): string[] {
  const n = items.length;
  const lengths = items.map((s) => s.length);
  const assigned = new Array<number>(n).fill(0);
  let active = lengths.map((_, i) => i).filter((i) => lengths[i] > 0);
  let remaining = total;
  // Each pass: split remaining budget evenly among items still under
  // their natural length. Items that hit their full length drop out;
  // their unused portion gets redistributed in the next pass.
  while (active.length > 0 && remaining > 0) {
    const share = remaining / active.length;
    const stillActive: number[] = [];
    let used = 0;
    for (const i of active) {
      const want = lengths[i] - assigned[i];
      const give = Math.min(want, share);
      assigned[i] += give;
      used += give;
      if (assigned[i] < lengths[i]) stillActive.push(i);
    }
    remaining -= used;
    if (stillActive.length === active.length) break;
    active = stillActive;
  }
  return items.map((s, i) => {
    const cap = Math.max(1, Math.floor(assigned[i]));
    return s.length <= cap ? s : s.slice(0, cap - 1) + "…";
  });
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

// Node rect colors keyed on the lifecycle state. Pending / skipped /
// cancelled used to all read as one wash of slate; the palette below
// pushes them apart on the lightness axis so operators can spot each
// at a glance:
//   skipped     -> lightest ghosted grey (deliberately not run)
//   pending     -> dim slate (hasn't started)
//   cancelled   -> charcoal, more solid (stopped with prejudice)
// Running, success, failed, cached keep their dedicated hue.
function dagNodeColors(
  n: RunNode,
  isSelected: boolean,
): { fill: string; border: string } {
  const k = n.outcome || n.status;
  let fill = "rgba(100,116,139,0.08)";
  let border = "rgba(100,116,139,0.30)";
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
      // Charcoal: stopped with prejudice. Darker + more solid than
      // pending or skipped so it reads as a deliberate halt.
      fill = "rgba(30,41,59,0.45)";
      border = "rgba(71,85,105,0.75)";
      break;
    case "cached":
      fill = "rgba(139,92,246,0.12)";
      border = "rgba(167,139,250,0.55)";
      break;
    case "skipped":
      // Lightest ghosted grey: intentionally not run. Pushes lighter
      // than the pending default so the eye reads "decided to skip"
      // versus "waiting to start".
      fill = "rgba(148,163,184,0.04)";
      border = "rgba(148,163,184,0.25)";
      break;
    case "skipped-concurrent":
      // OnLimit:Skip -- slot was full, not a deliberate skip. Sits
      // between pending and cancelled in weight so it reads as
      // "blocked from running" rather than "chose not to run".
      fill = "rgba(71,85,105,0.22)";
      border = "rgba(100,116,139,0.6)";
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
      // Light dot on the charcoal rect reads as "stopped".
      return "fill-slate-300";
    case "cached":
      return "fill-violet-400";
    case "skipped":
      // Faint dot on the ghosted rect -- "decided to skip".
      return "fill-slate-400";
    case "skipped-concurrent":
      return "fill-slate-500";
    case "superseded":
      return "fill-amber-500";
    case "approval_pending":
      return "fill-yellow-400 animate-pulse";
    default:
      // pending (no outcome, no running status yet)
      return "fill-slate-600";
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
