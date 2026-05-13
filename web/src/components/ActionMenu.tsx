"use client";

import { useEffect, useRef, useState, type ReactNode } from "react";

// ActionMenuItem is one row of an ActionMenu popover. `tone` shifts
// the row's hover accent; "danger" picks rose, "primary" indigo, the
// default a neutral slate. `description` renders as a muted second
// line so an item can carry a one-line hint without a tooltip.
export interface ActionMenuItem {
  label: string;
  description?: string;
  tone?: "default" | "primary" | "danger";
  disabled?: boolean;
  onSelect: () => void;
}

// ActionMenu anchors a popover to its trigger and closes on outside
// click, Escape, or item selection. Use for compact two-or-three
// choice menus next to a button -- the popover stays visually tied
// to the dashboard's surfaces instead of dropping out to a native
// browser confirm() dialog.
export default function ActionMenu({
  trigger,
  items,
  align = "end",
  title,
}: {
  trigger: (open: boolean, toggle: () => void) => ReactNode;
  items: ActionMenuItem[];
  // Horizontal alignment of the popover relative to the trigger.
  // "end" right-aligns (good for top-bar action stacks); "start"
  // left-aligns.
  align?: "start" | "end";
  // Optional small label rendered above the items.
  title?: string;
}) {
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLSpanElement | null>(null);

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

  const toggle = () => setOpen((v) => !v);

  return (
    <span ref={wrapRef} className="relative inline-block">
      {trigger(open, toggle)}
      {open && (
        <div
          role="menu"
          className={`absolute z-50 mt-1 min-w-[180px] rounded-md border border-[var(--border)] bg-[var(--surface)] shadow-xl py-1 ${
            align === "end" ? "right-0" : "left-0"
          }`}
        >
          {title && (
            <div className="px-3 py-1 text-[10px] font-semibold uppercase tracking-wider text-[var(--muted)]">
              {title}
            </div>
          )}
          {items.map((it, idx) => (
            <button
              key={idx}
              role="menuitem"
              disabled={it.disabled}
              onClick={(e) => {
                e.stopPropagation();
                if (it.disabled) return;
                setOpen(false);
                it.onSelect();
              }}
              className={`w-full text-left px-3 py-1.5 text-xs disabled:opacity-40 disabled:cursor-not-allowed transition-colors ${toneClass(it.tone)}`}
            >
              <div className="font-medium leading-tight">{it.label}</div>
              {it.description && (
                <div className="text-[10px] text-[var(--muted)] mt-0.5 leading-snug">
                  {it.description}
                </div>
              )}
            </button>
          ))}
        </div>
      )}
    </span>
  );
}

function toneClass(tone?: "default" | "primary" | "danger"): string {
  switch (tone) {
    case "primary":
      return "text-[var(--foreground)] hover:bg-indigo-500/15 hover:text-indigo-200";
    case "danger":
      return "text-[var(--foreground)] hover:bg-rose-500/15 hover:text-rose-200";
    default:
      return "text-[var(--foreground)] hover:bg-[var(--surface-raised)]";
  }
}
