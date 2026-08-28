"use client";

import { useEffect, useState } from "react";

export type ToastKind = "info" | "success" | "error";

export interface Toast {
  id: number;
  kind: ToastKind;
  text: string;
  ttl?: number;
}

type Listener = (toasts: Toast[]) => void;

let counter = 1;
let toasts: Toast[] = [];
const listeners = new Set<Listener>();

function notify() {
  for (const fn of listeners) fn(toasts);
}

export function toast(text: string, kind: ToastKind = "info", ttl = 3500) {
  const id = counter++;
  toasts = [...toasts, { id, text, kind, ttl }];
  notify();
  if (ttl > 0) {
    window.setTimeout(() => dismiss(id), ttl);
  }
  return id;
}

export function dismiss(id: number) {
  toasts = toasts.filter((t) => t.id !== id);
  notify();
}

export default function Toaster() {
  const [list, setList] = useState<Toast[]>(toasts);
  useEffect(() => {
    const fn: Listener = (next) => setList(next);
    listeners.add(fn);
    return () => {
      listeners.delete(fn);
    };
  }, []);

  if (list.length === 0) return null;

  return (
    <div className="fixed bottom-4 right-4 z-50 flex flex-col gap-2 pointer-events-none">
      {list.map((t) => (
        <div
          key={t.id}
          className={`pointer-events-auto rounded-md border px-3 py-2 text-xs shadow-lg backdrop-blur-sm flex items-start gap-2 max-w-sm ${accent(t.kind)}`}
        >
          <span className="flex-1 leading-snug">{t.text}</span>
          <button
            onClick={() => dismiss(t.id)}
            aria-label="dismiss"
            className="opacity-60 hover:opacity-100 transition-opacity"
          >
            ✕
          </button>
        </div>
      ))}
    </div>
  );
}

function accent(kind: ToastKind): string {
  switch (kind) {
    case "success":
      return "bg-emerald-500/15 border-emerald-400/40 text-emerald-100";
    case "error":
      return "bg-rose-500/15 border-rose-400/40 text-rose-100";
    default:
      return "bg-slate-500/20 border-slate-400/40 text-slate-100";
  }
}
