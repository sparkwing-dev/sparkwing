"use client";

import { useEffect, useState } from "react";
import { type Node, type NodeWorkStep, type Run } from "@/lib/api";

// Gantt-ish execution timeline. Each row is a node; bar position +
// width come from the node's started_at / finished_at. Running
// nodes get an open-ended bar up to now. Skipped/pending nodes are
// omitted (no timeline presence). Step bars are read from the
// structured NodeWorkStep records on each node (no log parsing).

function fmtMs(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  const m = Math.floor(s / 60);
  return `${m}m ${Math.round(s % 60)}s`;
}

interface Row {
  id: string;
  outcome: string;
  startMs: number;
  durationMs: number;
  running: boolean;
}

function outcomeColor(outcome: string, running: boolean): string {
  if (running) return "bg-indigo-400/70";
  switch (outcome) {
    case "success":
      return "bg-green-400/70";
    case "failed":
      return "bg-red-400/70";
    case "cancelled":
      return "bg-amber-400/70";
    case "cached":
      return "bg-violet-400/70";
    case "satisfied":
      return "bg-cyan-400/70";
    case "skipped":
      return "bg-slate-400/40";
    case "skipped-concurrent":
      // OnLimit:Skip. Distinct from skipped so operators can
      // see the slot was full, not a SkipIf predicate.
      return "bg-slate-500/40";
    case "superseded":
      // OnLimit:CancelOthers. Distinct from cancelled so
      // operators can see "evicted by newer run" vs operator cancel.
      return "bg-amber-500/60";
    default:
      return "bg-slate-400/60";
  }
}

function extractRows(nodes: Node[]): {
  rows: Row[];
  zero: number;
  totalMs: number;
} {
  const withStart = nodes.filter((n) => !!n.started_at);
  if (withStart.length === 0) return { rows: [], zero: 0, totalMs: 0 };
  const starts = withStart.map((n) => new Date(n.started_at!).getTime());
  const zero = Math.min(...starts);
  const now = Date.now();
  const ends = withStart.map((n) =>
    n.finished_at ? new Date(n.finished_at).getTime() : now,
  );
  const totalMs = Math.max(...ends) - zero;
  const rows: Row[] = withStart.map((n) => {
    const s = new Date(n.started_at!).getTime();
    const e = n.finished_at ? new Date(n.finished_at).getTime() : now;
    return {
      id: n.id,
      outcome: n.outcome || n.status,
      startMs: s - zero,
      durationMs: Math.max(1, e - s),
      running: !n.finished_at,
    };
  });
  return { rows, zero, totalMs };
}

interface StepRow {
  name: string;
  startMs: number;
  durationMs: number;
  status: "passed" | "failed" | "running" | "skipped";
}

// stepsForNode pulls every NodeWorkStep with timing data and projects
// it onto the run-wide timeline (offsets relative to `zero`). Steps
// without started_at are excluded — they never ran or haven't run yet.
function stepsForNode(node: Node, zero: number): StepRow[] {
  const work = node.work;
  if (!work || !work.steps) return [];
  const now = Date.now();
  const out: StepRow[] = [];
  for (const s of work.steps as NodeWorkStep[]) {
    if (!s.started_at) continue;
    const start = new Date(s.started_at).getTime();
    let duration = s.duration_ms ?? 0;
    if (!duration && s.status === "running") {
      duration = Math.max(0, now - start);
    }
    if (!duration && s.finished_at) {
      duration = Math.max(0, new Date(s.finished_at).getTime() - start);
    }
    out.push({
      name: s.id,
      startMs: start - zero,
      durationMs: Math.max(1, duration),
      status: (s.status as StepRow["status"]) || "passed",
    });
  }
  return out;
}

function stepBarColor(status: StepRow["status"]): string {
  switch (status) {
    case "failed":
      return "bg-red-300/80";
    case "running":
      return "bg-indigo-300/80";
    case "skipped":
      return "bg-slate-400/40";
    default:
      return "bg-green-300/80";
  }
}

export default function ExecutionWaterfall({
  run,
  nodes,
  focusNode,
  focusStep,
  onSelectNode,
  onSelectStep,
}: {
  run: Run;
  nodes: Node[];
  focusNode?: string | null;
  focusStep?: string | null;
  onSelectNode?: (id: string | null) => void;
  onSelectStep?: (nodeId: string, stepId: string | null) => void;
}) {
  const { rows, totalMs, zero } = extractRows(nodes);
  const nodeById = new Map(nodes.map((n) => [n.id, n]));
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  // Auto-expand the focused node when a step in it is selected so
  // the step bars reveal without manual toggle.
  useEffect(() => {
    if (!focusStep || !focusNode) return;
    setExpanded((prev) => {
      if (prev.has(focusNode)) return prev;
      const next = new Set(prev);
      next.add(focusNode);
      return next;
    });
  }, [focusStep, focusNode]);

  const toggle = (id: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  if (rows.length === 0) {
    return (
      <div className="text-xs text-[var(--muted)] p-4">
        No node timing data yet.
      </div>
    );
  }

  const barHeight = 20;
  const stepBarHeight = 12;
  const rowGap = 4;
  const labelWidth = 160;

  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-4">
      <div className="flex items-center justify-between mb-3">
        <div className="text-xs font-medium text-[var(--muted)]">
          Execution Timeline
        </div>
        <div className="text-[10px] text-[var(--muted)] font-mono">
          run {run.id.slice(-12)} · {rows.length} node
          {rows.length !== 1 ? "s" : ""}
        </div>
      </div>
      <div className="flex gap-2">
        <div className="shrink-0 flex flex-col" style={{ width: labelWidth }}>
          {rows.map((r) => {
            const node = nodeById.get(r.id);
            const steps = node ? stepsForNode(node, zero) : [];
            const isOpen = expanded.has(r.id);
            const isFocus = focusNode === r.id;
            return (
              <div key={r.id} className="flex flex-col">
                <div
                  className={`flex items-center gap-1 text-left transition-colors ${isFocus ? "bg-amber-400/10 -mx-1 px-1 rounded" : ""}`}
                  style={{ height: barHeight, marginBottom: rowGap }}
                >
                  <button
                    onClick={() => steps.length > 0 && toggle(r.id)}
                    className={`w-3 text-center text-[var(--muted)] ${steps.length > 0 ? "cursor-pointer hover:text-cyan-300" : "cursor-default"}`}
                    title={
                      steps.length > 0
                        ? isOpen
                          ? "collapse steps"
                          : "expand steps"
                        : ""
                    }
                  >
                    {steps.length > 0 ? (isOpen ? "▾" : "▸") : ""}
                  </button>
                  <button
                    onClick={() => onSelectNode?.(isFocus ? null : r.id)}
                    className={`text-[11px] truncate font-mono text-left flex-1 min-w-0 ${onSelectNode ? "cursor-pointer hover:text-cyan-300" : "cursor-default"} ${isFocus ? "text-amber-300" : "text-[var(--foreground)]"}`}
                    title={r.id}
                  >
                    {r.id}
                  </button>
                  {steps.length > 0 && (
                    <span className="text-[9px] text-[var(--muted)] shrink-0">
                      ({steps.length})
                    </span>
                  )}
                </div>
                {isOpen &&
                  steps.map((s, si) => {
                    const stepFocus =
                      isFocus && focusStep != null && s.name === focusStep;
                    return (
                      <button
                        key={`${r.id}-${si}-${s.name}`}
                        onClick={() =>
                          onSelectStep?.(r.id, stepFocus ? null : s.name)
                        }
                        className={`text-[10px] truncate font-mono pl-4 flex items-center text-left ${stepFocus ? "text-amber-300 bg-amber-400/10 -mx-1 px-1 rounded" : "text-[var(--muted)]"} ${onSelectStep ? "cursor-pointer hover:text-cyan-300" : "cursor-default"}`}
                        style={{
                          height: stepBarHeight + 4,
                          marginBottom: rowGap,
                        }}
                        title={s.name}
                      >
                        {s.name}
                      </button>
                    );
                  })}
              </div>
            );
          })}
        </div>
        <div className="flex-1 relative min-w-[280px]">
          {rows.map((r) => {
            const node = nodeById.get(r.id);
            const steps = node ? stepsForNode(node, zero) : [];
            const isOpen = expanded.has(r.id);
            const isFocus = focusNode === r.id;
            const left = (r.startMs / totalMs) * 100;
            const width = Math.max(0.3, (r.durationMs / totalMs) * 100);
            return (
              <div key={r.id} className="flex flex-col">
                <div
                  className={`flex items-center ${isFocus ? "bg-amber-400/10" : ""}`}
                  style={{ height: barHeight, marginBottom: rowGap }}
                >
                  <div className="relative w-full h-full">
                    <div
                      onClick={() => onSelectNode?.(isFocus ? null : r.id)}
                      className={`absolute h-full rounded ${outcomeColor(r.outcome, r.running)} ${r.running ? "animate-pulse" : ""} ${isFocus ? "ring-2 ring-amber-400" : ""} ${onSelectNode ? "cursor-pointer" : ""}`}
                      style={{ left: `${left}%`, width: `${width}%` }}
                      title={`${r.id}: ${fmtMs(r.durationMs)}${r.running ? " (running)" : ""}`}
                    />
                  </div>
                </div>
                {isOpen &&
                  steps.map((s, si) => {
                    const sLeft = (s.startMs / totalMs) * 100;
                    const sWidth = Math.max(
                      0.3,
                      ((s.durationMs || 1) / totalMs) * 100,
                    );
                    const stepFocus =
                      isFocus && focusStep != null && s.name === focusStep;
                    return (
                      <div
                        key={`${r.id}-${si}-${s.name}`}
                        className={`flex items-center ${stepFocus ? "bg-amber-400/10" : ""}`}
                        style={{
                          height: stepBarHeight + 4,
                          marginBottom: rowGap,
                        }}
                      >
                        <div className="relative w-full h-full">
                          <div
                            onClick={() =>
                              onSelectStep?.(r.id, stepFocus ? null : s.name)
                            }
                            className={`absolute rounded ${stepBarColor(s.status)} ${s.status === "running" ? "animate-pulse" : ""} ${stepFocus ? "ring-2 ring-amber-400" : ""} ${onSelectStep ? "cursor-pointer" : ""}`}
                            style={{
                              left: `${sLeft}%`,
                              width: `${sWidth}%`,
                              top: 2,
                              height: stepBarHeight,
                            }}
                            title={`${s.name}: ${fmtMs(s.durationMs || 0)}${s.status === "running" ? " (running)" : ""}`}
                          />
                        </div>
                      </div>
                    );
                  })}
              </div>
            );
          })}
          <div className="flex justify-between text-[10px] text-[var(--muted)] mt-1 pt-1 border-t border-[var(--border)]">
            <span>0</span>
            <span>{fmtMs(totalMs / 2)}</span>
            <span>{fmtMs(totalMs)}</span>
          </div>
        </div>
        <div className="shrink-0 flex flex-col" style={{ width: 60 }}>
          {rows.map((r) => {
            const node = nodeById.get(r.id);
            const steps = node ? stepsForNode(node, zero) : [];
            const isOpen = expanded.has(r.id);
            return (
              <div key={r.id} className="flex flex-col">
                <div
                  className="text-[10px] font-mono text-[var(--muted)] flex items-center justify-end"
                  style={{ height: barHeight, marginBottom: rowGap }}
                >
                  {fmtMs(r.durationMs)}
                </div>
                {isOpen &&
                  steps.map((s, si) => (
                    <div
                      key={`${r.id}-${si}-${s.name}`}
                      className="text-[9px] font-mono text-[var(--muted)] flex items-center justify-end"
                      style={{
                        height: stepBarHeight + 4,
                        marginBottom: rowGap,
                      }}
                    >
                      {fmtMs(s.durationMs || 0)}
                    </div>
                  ))}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
