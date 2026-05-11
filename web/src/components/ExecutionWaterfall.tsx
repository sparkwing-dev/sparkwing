"use client";

import { useEffect, useState } from "react";
import { type Node, type Run, getRunLogs } from "@/lib/api";
import { parseLogLines, type StepSection } from "@/lib/logParser";

// Gantt-ish execution timeline. Each row is a node; bar position +
// width come from the node's started_at / finished_at. Running
// nodes get an open-ended bar up to now. Skipped/pending nodes are
// omitted (no timeline presence).

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
  return { rows, zero, totalMs: Math.max(1, totalMs) };
}

interface StepRow {
  name: string; // step display name (no node prefix)
  startMs: number;
  durationMs: number;
  status: "passed" | "failed" | "running";
}

export default function ExecutionWaterfall({
  run,
  nodes,
}: {
  run: Run;
  nodes: Node[];
}) {
  const { rows, totalMs, zero } = extractRows(nodes);
  // Pulled run logs let us overlay step-level bars under each node.
  // Single fetch keyed on (runID, isTerminal): for terminal runs the
  // log set is final; for in-flight runs we just snapshot once on tab
  // entry (kept lightweight — re-fetch on demand if needed).
  const [stepsByNode, setStepsByNode] = useState<Record<string, StepRow[]>>({});
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const isTerminal =
    run.status === "success" ||
    run.status === "failed" ||
    run.status === "cancelled";
  useEffect(() => {
    let cancelled = false;
    (async () => {
      const text = await getRunLogs(run.id);
      if (cancelled || !text) return;
      const parsed = parseLogLines(text.split("\n"));
      const map: Record<string, StepRow[]> = {};
      for (const raw of parsed.sections) {
        if (raw.type !== "step") continue;
        const s = raw as StepSection;
        // Step-scope sections are named "{nodeId} · {stepName}".
        // Node-scope sections (just "{nodeId}") are the setup block;
        // skip them — they're already represented by the node bar.
        const idx = s.name.indexOf(" · ");
        if (idx < 0) continue;
        const nodeId = s.name.slice(0, idx);
        const stepName = s.name.slice(idx + 3);
        if (s.startedAtMs == null) continue;
        const arr = map[nodeId] ?? [];
        arr.push({
          name: stepName,
          startMs: s.startedAtMs - zero,
          durationMs: s.durationMs ?? 0,
          status: s.status,
        });
        map[nodeId] = arr;
      }
      setStepsByNode(map);
    })();
    return () => {
      cancelled = true;
    };
  }, [run.id, zero, isTerminal]);

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

  const stepColor = (status: "passed" | "failed" | "running"): string => {
    if (status === "failed") return "bg-red-300/80";
    if (status === "running") return "bg-indigo-300/80";
    return "bg-green-300/80";
  };

  const barHeight = 20;
  const stepBarHeight = 12;
  const rowGap = 4;
  const labelWidth = 140;

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
            const steps = stepsByNode[r.id] || [];
            const isOpen = expanded.has(r.id);
            return (
              <div key={r.id} className="flex flex-col">
                <button
                  onClick={() => steps.length > 0 && toggle(r.id)}
                  className={`text-[11px] text-[var(--foreground)] truncate flex items-center gap-1 font-mono ${steps.length > 0 ? "cursor-pointer hover:text-cyan-300" : "cursor-default"} text-left`}
                  style={{ height: barHeight, marginBottom: rowGap }}
                  title={r.id}
                >
                  <span className="w-3 text-center text-[var(--muted)]">
                    {steps.length > 0 ? (isOpen ? "▾" : "▸") : ""}
                  </span>
                  <span className="truncate">{r.id}</span>
                  {steps.length > 0 && (
                    <span className="text-[9px] text-[var(--muted)] shrink-0">
                      ({steps.length})
                    </span>
                  )}
                </button>
                {isOpen &&
                  steps.map((s, si) => (
                    <div
                      key={`${r.id}-${si}-${s.name}`}
                      className="text-[10px] text-[var(--muted)] truncate font-mono pl-4 flex items-center"
                      style={{
                        height: stepBarHeight + 4,
                        marginBottom: rowGap,
                      }}
                      title={s.name}
                    >
                      {s.name}
                    </div>
                  ))}
              </div>
            );
          })}
        </div>
        <div className="flex-1 relative min-w-[280px]">
          {rows.map((r) => {
            const steps = stepsByNode[r.id] || [];
            const isOpen = expanded.has(r.id);
            const left = (r.startMs / totalMs) * 100;
            const width = Math.max(0.3, (r.durationMs / totalMs) * 100);
            return (
              <div key={r.id} className="flex flex-col">
                <div
                  className="flex items-center"
                  style={{ height: barHeight, marginBottom: rowGap }}
                >
                  <div className="relative w-full h-full">
                    <div
                      className={`absolute h-full rounded ${outcomeColor(r.outcome, r.running)} ${r.running ? "animate-pulse" : ""}`}
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
                    return (
                      <div
                        key={`${r.id}-${si}-${s.name}`}
                        className="flex items-center"
                        style={{
                          height: stepBarHeight + 4,
                          marginBottom: rowGap,
                        }}
                      >
                        <div className="relative w-full h-full">
                          <div
                            className={`absolute rounded ${stepColor(s.status)} ${s.status === "running" ? "animate-pulse" : ""}`}
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
            const steps = stepsByNode[r.id] || [];
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
