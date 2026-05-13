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

  useEffect(() => {
    let cancelled = false;
    getRunAttempts(currentRunID).then((rows) => {
      if (cancelled) return;
      setAttempts(rows);
    });
    return () => {
      cancelled = true;
    };
  }, [currentRunID, refreshKey]);

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
