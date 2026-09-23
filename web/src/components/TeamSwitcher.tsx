"use client";

import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import { type Role, switchTeam } from "@/lib/teams";
import { useTeamState } from "@/lib/useTeam";
import { toast } from "@/components/Toasts";
import {
  rememberTeamNotice,
  switchedNotice,
  takeTeamNotice,
} from "@/lib/teamNotice";

export function RolePill({ role }: { role: Role }) {
  const tone =
    role === "owner"
      ? "bg-indigo-500/20 text-indigo-300"
      : role === "editor"
        ? "bg-emerald-500/15 text-emerald-300"
        : "bg-slate-500/20 text-slate-300";
  return (
    <span
      className={`px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide rounded-sm ${tone}`}
    >
      {role}
    </span>
  );
}

export default function TeamSwitcher() {
  const state = useTeamState();
  const [open, setOpen] = useState(false);
  const [switching, setSwitching] = useState<string | null>(null);
  const root = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const notice = takeTeamNotice();
    if (notice) toast(notice, "success", 6000);
  }, []);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (root.current && !root.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  if (state.status !== "ready") return null;
  const { me } = state;
  const active = me.active_team;

  async function choose(slug: string) {
    if (slug === active.slug) {
      setOpen(false);
      return;
    }
    setSwitching(slug);
    try {
      await switchTeam(slug);
      const to = me.memberships.find((m) => m.slug === slug);
      rememberTeamNotice(switchedNotice(to?.display_name ?? slug));
      window.location.reload();
    } catch (err) {
      setSwitching(null);
      toast(err instanceof Error ? err.message : String(err), "error");
    }
  }

  return (
    <div ref={root} className="relative ml-3 flex items-center">
      <span aria-hidden="true" className="text-[var(--border)] text-lg mr-2">
        /
      </span>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="menu"
        aria-expanded={open}
        title="Active team"
        className="flex items-center gap-2 px-2 py-1 rounded-[var(--radius-control)] hover:bg-[var(--surface-raised)] text-sm"
      >
        <span className="font-medium max-w-[14rem] truncate">
          {active.display_name}
        </span>
        <RolePill role={active.role} />
        {me.invitations.length > 0 ? (
          <span
            className="w-2 h-2 rounded-full bg-[var(--warning)]"
            title={`${me.invitations.length} pending invitation(s)`}
          />
        ) : null}
        <svg
          aria-hidden="true"
          viewBox="0 0 12 12"
          className="w-3 h-3 text-[var(--muted)]"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.5"
        >
          <path d="M3 4.5 6 7.5l3-3" />
        </svg>
      </button>
      {open ? (
        <div
          role="menu"
          className="absolute left-4 top-full mt-1 w-72 bg-[var(--surface)] border border-[var(--border)] rounded-[var(--radius-panel)] shadow-lg z-50 overflow-hidden"
        >
          <div className="px-3 py-2 border-b border-[var(--border)]">
            <div className="text-xs text-[var(--muted)]">Signed in as</div>
            <div className="text-sm truncate">{me.user.email}</div>
          </div>
          <div className="py-1">
            <div className="px-3 pt-1 pb-1 text-[11px] uppercase tracking-wide text-[var(--muted)]">
              Teams
            </div>
            {me.memberships.map((m) => (
              <button
                key={m.slug}
                type="button"
                role="menuitemradio"
                aria-checked={m.slug === active.slug}
                disabled={switching !== null}
                onClick={() => choose(m.slug)}
                className="w-full flex items-center gap-2 px-3 py-1.5 text-sm text-left hover:bg-[var(--surface-raised)] disabled:opacity-60"
              >
                <span className="w-3 text-[var(--accent)]">
                  {m.slug === active.slug ? "✓" : ""}
                </span>
                <span className="flex-1 truncate">{m.display_name}</span>
                {switching === m.slug ? (
                  <span className="text-xs text-[var(--muted)]">
                    switching…
                  </span>
                ) : (
                  <RolePill role={m.role} />
                )}
              </button>
            ))}
          </div>
          {me.invitations.length > 0 ? (
            <Link
              href="/invitations"
              onClick={() => setOpen(false)}
              className="flex items-center justify-between px-3 py-2 text-sm border-t border-[var(--border)] bg-amber-500/10 text-amber-200 hover:bg-amber-500/20"
            >
              <span>Pending invitations</span>
              <span className="text-xs">{me.invitations.length}</span>
            </Link>
          ) : null}
          <div className="py-1 border-t border-[var(--border)]">
            {[
              { href: "/team", label: "Members and settings" },
              { href: "/team/machines", label: "Connect a machine" },
              { href: "/team/new", label: "Create a team" },
            ].map((item) => (
              <Link
                key={item.href}
                href={item.href}
                role="menuitem"
                onClick={() => setOpen(false)}
                className="block px-3 py-1.5 text-sm text-[var(--muted)] hover:text-[var(--foreground)] hover:bg-[var(--surface-raised)]"
              >
                {item.label}
              </Link>
            ))}
          </div>
        </div>
      ) : null}
    </div>
  );
}
