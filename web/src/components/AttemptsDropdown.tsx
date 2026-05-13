"use client";

import { useEffect, useRef, useState } from "react";
import { type Run, getRunAttempts } from "@/lib/api";

// Fires the page-level select-run event so the runs list page swaps
// to a different attempt. Keep this string in sync with
// SELECT_RUN_EVENT defined in app/runs/page.tsx.
const SELECT_RUN_EVENT = "sparkwing:select-run";

// AttemptsDropdown lists every run sharing the same retry-tree root
// as `currentRunID`, numbered "Attempt N" by chronological order
// (oldest = #1). Branches (one source rerun multiple times) appear as
// siblings ordered by created_at, so the linear numbering stays
// monotonic even when the underlying retry_of graph forks.
//
// Behaviour:
//   - Hidden entirely when there's no retry history (just one
//     attempt). The trigger button only paints once a 2nd run lands.
//   - Compact variant (`dense`) for inline row use; the default
//     variant pairs with action buttons in the detail header.
//
// The dropdown navigates by dispatching SELECT_RUN_EVENT so the URL,
// scroll, and checkbox highlight all stay in sync with manual row
// clicks.
export default function AttemptsDropdown({
  currentRunID,
  dense = false,
  refreshKey,
}: {
  currentRunID: string;
  // dense: smaller chip suitable for inline list rows.
  dense?: boolean;
  // refreshKey is bumped by the caller to force a refetch (e.g. after
  // a rerun is queued). Any change re-runs the fetch effect.
  refreshKey?: number;
}) {
  const [attempts, setAttempts] = useState<Run[]>([]);
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLSpanElement | null>(null);

  // Bumped whenever a "sparkwing:runs-changed" event fires so a
  // rerun queued from elsewhere on the page refreshes this dropdown
  // without the user having to navigate away and back.
  const [bus, setBus] = useState(0);
  useEffect(() => {
    const handler = () => setBus((v) => v + 1);
    window.addEventListener("sparkwing:runs-changed", handler);
    return () => window.removeEventListener("sparkwing:runs-changed", handler);
  }, []);

  useEffect(() => {
    let cancelled = false;
    getRunAttempts(currentRunID).then((rows) => {
      if (cancelled) return;
      setAttempts(rows);
    });
    return () => {
      cancelled = true;
    };
  }, [currentRunID, refreshKey, bus]);

  useEffect(() => {
    if (!open) return;
    function onDown(e: MouseEvent) {
      if (!wrapRef.current) return;
      if (!wrapRef.current.contains(e.target as Node)) setOpen(false);
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") setOpen(false);
    }
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  // Single-attempt runs (no rerun in either direction): hide the
  // dropdown. A standalone "Attempt 1 of 1" pill is noise.
  if (attempts.length <= 1) return null;

  const currentIdx = attempts.findIndex((a) => a.id === currentRunID);
  const total = attempts.length;
  const label =
    currentIdx >= 0
      ? `Attempt ${currentIdx + 1} of ${total}`
      : `${total} attempts`;

  const triggerCls = dense
    ? "text-[9px] px-1.5 py-px"
    : "text-xs px-2 py-1 font-medium";

  return (
    <span ref={wrapRef} className="relative inline-block">
      <button
        onClick={(e) => {
          e.stopPropagation();
          setOpen((v) => !v);
        }}
        aria-expanded={open}
        className={`${triggerCls} rounded border transition-colors inline-flex items-center gap-1 font-mono ${
          open
            ? "bg-amber-500/30 text-amber-100 border-amber-400"
            : "bg-amber-500/15 text-amber-300 border-amber-500/40 hover:bg-amber-500/25"
        }`}
        title="Switch between rerun attempts"
      >
        {label}
        <span aria-hidden className="text-[10px] opacity-70">
          ▾
        </span>
      </button>
      {open && (
        <div
          role="menu"
          className="absolute right-0 z-50 mt-1 min-w-[260px] rounded-md border border-[var(--border)] bg-[var(--surface)] shadow-xl py-1"
        >
          <div className="px-3 py-1 text-[10px] font-semibold uppercase tracking-wider text-[var(--muted)]">
            Attempts
          </div>
          {attempts.map((a, i) => {
            const isCurrent = a.id === currentRunID;
            const mode = attemptMode(a);
            return (
              <button
                key={a.id}
                role="menuitem"
                onClick={(e) => {
                  e.stopPropagation();
                  setOpen(false);
                  if (isCurrent) return;
                  window.dispatchEvent(
                    new CustomEvent<string>(SELECT_RUN_EVENT, {
                      detail: a.id,
                    }),
                  );
                }}
                className={`w-full text-left px-3 py-1.5 text-xs transition-colors flex items-center gap-2 ${
                  isCurrent
                    ? "bg-amber-500/15 text-amber-100 cursor-default"
                    : "text-[var(--foreground)] hover:bg-[var(--surface-raised)]"
                }`}
              >
                <span
                  className={`inline-block w-2 h-2 rounded-full shrink-0 ${statusDot(a.status)}`}
                  aria-hidden
                />
                <span className="font-mono shrink-0">#{i + 1}</span>
                <span className="text-[var(--muted)] truncate flex-1">
                  {fmtAttemptTime(a)}
                </span>
                {mode && (
                  <span
                    className={`text-[9px] uppercase font-mono px-1 py-px rounded border shrink-0 ${
                      mode === "full"
                        ? "border-amber-500/40 bg-amber-500/10 text-amber-300"
                        : mode === "failed"
                          ? "border-emerald-500/40 bg-emerald-500/10 text-emerald-300"
                          : "border-[var(--border)] bg-[var(--surface-raised)] text-[var(--muted)]"
                    }`}
                    title={modeTooltip(mode)}
                  >
                    {modeLabel(mode)}
                  </span>
                )}
                {isCurrent && (
                  <span className="text-[10px] uppercase text-amber-300 shrink-0">
                    current
                  </span>
                )}
              </button>
            );
          })}
        </div>
      )}
    </span>
  );
}

function statusDot(status: string): string {
  switch (status) {
    case "success":
      return "bg-emerald-400";
    case "failed":
      return "bg-rose-400";
    case "cancelled":
      return "bg-slate-400";
    case "running":
      return "bg-indigo-400 animate-pulse";
    default:
      return "bg-slate-500";
  }
}

// attemptMode classifies a single run row by how it was started:
//   - "original"  the root attempt (no retry_of pointer)
//   - "full"      a retry that ran in Options.Full mode
//   - "failed"    a retry with skip-passed rehydration ("from failed")
//   - null        retry_of is set but invocation hasn't been stamped
//                 yet (the orchestrator writes flags only after the
//                 subprocess starts), so the mode is genuinely unknown
//                 for a brief window — render nothing rather than
//                 mislead.
type AttemptMode = "original" | "full" | "failed";

function attemptMode(r: Run): AttemptMode | null {
  if (!r.retry_of) return "original";
  const flags = r.invocation?.flags;
  if (!flags) return null;
  return flags.full === true ? "full" : "failed";
}

function modeLabel(m: AttemptMode): string {
  switch (m) {
    case "original":
      return "original";
    case "full":
      return "all";
    case "failed":
      return "from failed";
  }
}

function modeTooltip(m: AttemptMode): string {
  switch (m) {
    case "original":
      return "Original run — not a retry.";
    case "full":
      return "Rerun all: every node re-executed from scratch.";
    case "failed":
      return "Rerun from failed: passed nodes were reused; only failed/unreached nodes re-executed.";
  }
}

function fmtAttemptTime(r: Run): string {
  const t = r.started_at;
  if (!t) return r.id;
  try {
    const d = new Date(t);
    return `${d.toLocaleDateString(undefined, { month: "short", day: "numeric" })} ${d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}`;
  } catch {
    return r.id;
  }
}
