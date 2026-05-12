"use client";

// SummaryPanel renders the run's terminal-state rollup -- mirrors the
// CLI's `--- Summary ---` block. Visible whenever the run has
// finished (success / failed / cancelled). Shows: status pill +
// duration, per-outcome tally, jobs table (one row per node), errors
// block formatted with the same `<node> > <step> |` breadcrumb the
// CLI uses, and tip commands an operator/agent can copy back into a
// terminal.

import { useMemo, useState } from "react";
import Link from "next/link";
import type { Node as RunNode, Run, RunInvocation } from "@/lib/api";

function fmtMs(ms: number): string {
  if (!ms) return "-";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms - m * 60_000) / 1000);
  return `${m}m ${s}s`;
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

function durationMs(run: Run): number {
  if (!run.finished_at) return 0;
  return (
    new Date(run.finished_at).getTime() - new Date(run.started_at).getTime()
  );
}

// outcomeGlyph mirrors the CLI's outcomeIcon -- glyph + color class
// for each outcome the orchestrator can produce.
function outcomeGlyph(outcome: string): { glyph: string; cls: string } {
  switch (outcome) {
    case "success":
      return { glyph: "✓", cls: "text-green-400" };
    case "cached":
      return { glyph: "◈", cls: "text-cyan-400" };
    case "failed":
      return { glyph: "✗", cls: "text-red-400" };
    case "skipped":
    case "skipped-concurrent":
      return { glyph: "⊘", cls: "text-[var(--muted)]" };
    case "cancelled":
      return { glyph: "⊘", cls: "text-yellow-400" };
    case "superseded":
      return { glyph: "⟳", cls: "text-yellow-400" };
    default:
      return { glyph: "·", cls: "text-[var(--muted)]" };
  }
}

// summaryStatusGlyph: top-of-block run-status indicator.
function summaryStatusGlyph(status: string): { glyph: string; cls: string } {
  if (status === "success") return { glyph: "✓", cls: "text-green-400" };
  if (status === "failed") return { glyph: "✗", cls: "text-red-400" };
  if (status === "cancelled") return { glyph: "⊘", cls: "text-yellow-400" };
  return { glyph: "·", cls: "text-[var(--muted)]" };
}

// splitStepErrorPrefix matches the CLI's helper: lifts a `step "X": `
// prefix off an error string so the breadcrumb can render the step
// in its own column instead of repeating it inside the body.
function splitStepErrorPrefix(s: string): { step: string; body: string } {
  const prefix = 'step "';
  if (!s.startsWith(prefix)) return { step: "", body: s };
  const rest = s.slice(prefix.length);
  const end = rest.indexOf('": ');
  if (end < 0) return { step: "", body: s };
  return { step: rest.slice(0, end), body: rest.slice(end + 3) };
}

// CopyButton: shared with SetupPanel in spirit; redefined locally so
// each panel stays standalone-importable for Storybook.
function CopyButton({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      onClick={() => {
        void navigator.clipboard.writeText(value);
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      }}
      className={`text-[10px] uppercase tracking-wider font-mono px-1.5 py-0.5 rounded border transition-colors shrink-0 ${
        copied
          ? "border-green-500/40 text-green-400"
          : "border-[var(--border)] text-[var(--muted)] hover:text-[var(--foreground)] hover:border-[var(--muted)]"
      }`}
    >
      {copied ? "copied" : "copy"}
    </button>
  );
}

interface Tally {
  total: number;
  passed: number;
  failed: number;
  skipped: number;
  other: number;
}

// NodeAttrChips surfaces the DAG-pill attributes inline in the Jobs
// list so the summary view tells the same story as the DAG: dynamic
// shape, approval gates, cached runs, inline jobs, group membership,
// and cross-pipeline spawns. Each spawn entry is a deep link to the
// child run; everything else is informational.
export function NodeAttrChips({ n }: { n: RunNode }) {
  const chips: { label: string; cls: string; key: string }[] = [];
  if (n.dynamic)
    chips.push({
      key: "dynamic",
      label: "dynamic",
      cls: "bg-fuchsia-500/15 text-fuchsia-300",
    });
  if (n.approval)
    chips.push({
      key: "approval",
      label: "approval",
      cls: "bg-yellow-500/15 text-yellow-300",
    });
  if (n.outcome === "cached")
    chips.push({
      key: "cached",
      label: "cached",
      cls: "bg-violet-500/15 text-violet-300",
    });
  if (n.modifiers?.inline)
    chips.push({
      key: "inline",
      label: "inline",
      cls: "bg-slate-500/15 text-slate-300",
    });
  if (n.on_failure_of)
    chips.push({
      key: "onfail",
      label: `on-failure-of ${n.on_failure_of}`,
      cls: "bg-red-500/15 text-red-300",
    });
  if (n.groups && n.groups.length > 0)
    chips.push({
      key: "groups",
      label: `group: ${n.groups.join(", ")}`,
      cls: "bg-cyan-500/15 text-cyan-300",
    });
  return (
    <span className="flex items-center gap-1 flex-wrap">
      {chips.map((c) => (
        <span
          key={c.key}
          className={`px-1.5 rounded text-[10px] ${c.cls}`}
          title={c.label}
        >
          {c.label}
        </span>
      ))}
      {(n.spawned_pipelines ?? []).map((p) => (
        <Link
          key={p.child_run_id}
          href={`?run=${encodeURIComponent(p.child_run_id)}`}
          className="px-1.5 rounded text-[10px] bg-sky-500/20 text-sky-300 hover:bg-sky-500/30 transition-colors"
          title={`open spawned run for ${p.pipeline}`}
        >
          ↗ {p.pipeline}
        </Link>
      ))}
    </span>
  );
}

function buildTally(nodes: RunNode[]): Tally {
  const t: Tally = { total: 0, passed: 0, failed: 0, skipped: 0, other: 0 };
  for (const n of nodes) {
    t.total++;
    switch (n.outcome) {
      case "success":
      case "cached":
        t.passed++;
        break;
      case "failed":
        t.failed++;
        break;
      case "skipped":
      case "skipped-concurrent":
      case "cancelled":
        t.skipped++;
        break;
      default:
        t.other++;
    }
  }
  return t;
}

export default function SummaryPanel({
  run,
  nodes,
  collapsed,
  onToggle,
  inline = false,
}: {
  run: Run;
  nodes: RunNode[];
  collapsed: boolean;
  onToggle: () => void;
  inline?: boolean;
}) {
  const tally = useMemo(() => buildTally(nodes), [nodes]);
  const failed = useMemo(
    () => nodes.filter((n) => n.outcome === "failed" && n.error),
    [nodes],
  );
  const inv: RunInvocation = run.invocation ?? {};
  const hints = inv.hints ?? {};
  // Synthesize the standard three commands the CLI prints in its
  // Tips section so the panel works even on runs that predate the
  // hints-on-invocation column. Falls back to invocation-supplied
  // values when present so stale shapes don't drift.
  const tipCommands = [
    {
      label: "status",
      cmd: hints.status ?? `sparkwing runs status --run ${run.id}`,
    },
    {
      label: "logs",
      cmd: hints.logs ?? `sparkwing runs logs --run ${run.id}`,
    },
    ...(run.status === "failed"
      ? [
          {
            label: "retry",
            cmd: hints.retry ?? `sparkwing runs retry --run ${run.id}`,
          },
        ]
      : []),
  ];

  const statusG = summaryStatusGlyph(run.status);
  const dur = durationMs(run);

  return (
    <div className={inline ? "" : "border-b border-[var(--border)] shrink-0"}>
      {!inline && (
        <button
          onClick={onToggle}
          className="w-full flex items-center gap-2 px-4 py-2 text-xs text-[var(--muted)] hover:text-[var(--foreground)] transition-colors"
        >
          <span className="w-4 text-center">{collapsed ? "▸" : "▾"}</span>
          <span className="font-semibold text-[var(--foreground)]">
            Summary
          </span>
          <span className={`font-mono ${statusG.cls}`}>
            {statusG.glyph} {run.status}
          </span>
          {dur > 0 && (
            <span className="text-[var(--muted)] font-mono">
              ({fmtMs(dur)})
            </span>
          )}
        </button>
      )}
      {(inline || !collapsed) && (
        <div className="px-4 pb-3 space-y-3">
          {tally.total > 1 && (
            <div className="text-xs font-mono text-[var(--muted)]">
              {tally.total} node{tally.total === 1 ? "" : "s"}
              {tally.passed > 0 && (
                <>
                  {" · "}
                  <span className="text-green-400">{tally.passed} passed</span>
                </>
              )}
              {tally.failed > 0 && (
                <>
                  {" · "}
                  <span className="text-red-400">{tally.failed} failed</span>
                </>
              )}
              {tally.skipped > 0 && (
                <>
                  {" · "}
                  <span>{tally.skipped} skipped</span>
                </>
              )}
              {tally.other > 0 && (
                <>
                  {" · "}
                  <span className="text-yellow-400">{tally.other} other</span>
                </>
              )}
            </div>
          )}

          <div className="space-y-1">
            <div className="text-xs font-semibold text-[var(--foreground)]">
              Jobs
            </div>
            {nodes.map((n) => {
              const g = outcomeGlyph(n.outcome);
              return (
                <div
                  key={n.id}
                  className="flex items-center gap-2 text-xs font-mono"
                >
                  <span className={`w-4 text-center ${g.cls}`}>{g.glyph}</span>
                  <span className="truncate">{n.id}</span>
                  <NodeAttrChips n={n} />
                  <span className="flex-1" />
                  {/* Outcome word for non-success cases (mirrors the
                      CLI: success is unambiguous from the glyph;
                      anything else benefits from the label). */}
                  {n.outcome && n.outcome !== "success" && (
                    <span className={`${g.cls} text-[11px]`}>{n.outcome}</span>
                  )}
                  {nodeDuration(n) > 0 && (
                    <span className="text-[var(--muted)]">
                      {fmtMs(nodeDuration(n))}
                    </span>
                  )}
                </div>
              );
            })}
          </div>

          {failed.length > 0 && (
            <div className="space-y-2">
              <div className="text-xs font-semibold text-red-400">Errors</div>
              {failed.map((n) => {
                const { step, body } = splitStepErrorPrefix(n.error ?? "");
                const lines = body.split("\n");
                return (
                  <div key={n.id} className="text-xs font-mono space-y-0.5">
                    {/* First line carries the breadcrumb so multi-line
                         errors stay attributable. Matches the CLI's
                         `<node> › <step> │ <message>` layout. */}
                    <div>
                      <span className="text-[var(--foreground)]">{n.id}</span>
                      {step && (
                        <>
                          <span className="text-[var(--muted)]">{" › "}</span>
                          <span className="text-[var(--muted)]">{step}</span>
                        </>
                      )}
                      <span className="text-[var(--muted)]">{" │ "}</span>
                      <span className="text-red-300">{lines[0]}</span>
                    </div>
                    {lines.slice(1).map((l, i) => (
                      <div key={i} className="pl-4 text-red-300 break-all">
                        {l}
                      </div>
                    ))}
                  </div>
                );
              })}
            </div>
          )}

          {run.error && run.status !== "success" && (
            <div className="text-xs font-mono">
              <span className="text-[var(--muted)]">error </span>
              <span className="text-red-300">{run.error}</span>
            </div>
          )}

          <div className="space-y-1">
            <div className="text-xs font-semibold text-[var(--foreground)]">
              Tips
            </div>
            {tipCommands.map((t) => (
              <div key={t.label} className="flex items-center gap-2 text-xs">
                <span className="w-14 shrink-0 text-[var(--muted)] font-mono">
                  {t.label}
                </span>
                <code className="flex-1 px-2 py-1 rounded bg-[var(--surface)] border border-[var(--border)] text-cyan-300 font-mono break-all">
                  {t.cmd}
                </code>
                <CopyButton value={t.cmd} />
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
