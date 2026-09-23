"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { type ReactNode, useState } from "react";
import {
  type Capabilities,
  type Me,
  acceptInvitation,
  renameTeam,
} from "@/lib/teams";
import { githubAppEnabled } from "@/lib/githubApp";
import { refreshTeamState, useTeamState } from "@/lib/useTeam";
import { RolePill } from "@/components/TeamSwitcher";
import { toast } from "@/components/Toasts";

const baseTabs = [
  { href: "/team", label: "Members" },
  { href: "/team/machines", label: "Machines" },
];

export function teamTabs(caps: Capabilities | null) {
  return [
    ...baseTabs,
    ...(githubAppEnabled(caps)
      ? [{ href: "/team/github", label: "GitHub" }]
      : []),
    { href: "/team/billing", label: "Billing" },
  ];
}

export function Panel({
  title,
  hint,
  action,
  children,
}: {
  title: string;
  hint?: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="mb-6">
      <div className="flex items-end justify-between mb-2 gap-4">
        <div>
          <h2 className="text-xs font-bold uppercase tracking-wider text-[var(--muted)]">
            {title}
          </h2>
          {hint ? (
            <p className="text-xs text-[var(--muted)] mt-0.5">{hint}</p>
          ) : null}
        </div>
        {action}
      </div>
      <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg">
        {children}
      </div>
    </section>
  );
}

export function Notice({ children }: { children: ReactNode }) {
  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-4xl mx-auto w-full">
      <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-6 text-sm text-[var(--muted)]">
        {children}
      </div>
    </div>
  );
}

export const buttonClass =
  "px-3 py-1.5 text-sm rounded-[var(--radius-control)] bg-[var(--accent)] text-white hover:brightness-110 disabled:opacity-50 disabled:cursor-not-allowed";
export const quietButtonClass =
  "px-2 py-1 text-xs rounded-[var(--radius-control)] border border-[var(--border)] text-[var(--muted)] hover:text-[var(--foreground)] hover:bg-[var(--surface-raised)] disabled:opacity-50 disabled:cursor-not-allowed";
export const dangerButtonClass =
  "px-2 py-1 text-xs rounded-[var(--radius-control)] border border-red-500/40 text-red-300 hover:bg-red-500/10 disabled:opacity-50 disabled:cursor-not-allowed";
export const inputClass =
  "px-2.5 py-1.5 text-sm rounded-[var(--radius-control)] bg-[var(--background)] border border-[var(--border)] focus:outline-none focus:border-[var(--accent)]";

export function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export function InvitationsForMe({ me }: { me: Me }) {
  const [busy, setBusy] = useState<string | null>(null);
  if (me.invitations.length === 0) return null;
  async function accept(id: string) {
    setBusy(id);
    try {
      await acceptInvitation(id);
      window.location.reload();
    } catch (err) {
      setBusy(null);
      toast(errorText(err), "error");
    }
  }
  return (
    <div className="mb-6 rounded-lg border border-amber-500/40 bg-amber-500/10">
      {me.invitations.map((inv) => (
        <div
          key={inv.id}
          className="flex items-center gap-3 px-4 py-3 border-b border-amber-500/20 last:border-0"
        >
          <div className="flex-1 text-sm">
            You are invited to join{" "}
            <span className="font-medium">{inv.team_display_name}</span> as{" "}
            <RolePill role={inv.role} />
          </div>
          <button
            type="button"
            className={buttonClass}
            disabled={busy !== null}
            onClick={() => accept(inv.id)}
          >
            {busy === inv.id ? "Joining…" : "Accept"}
          </button>
        </div>
      ))}
    </div>
  );
}

export default function TeamShell({
  children,
}: {
  children: (me: Me, caps: Capabilities | null) => ReactNode;
}) {
  const state = useTeamState();
  const pathname = usePathname();
  if (state.status === "loading") {
    return <Notice>Loading team…</Notice>;
  }
  if (state.status === "single-team") {
    return (
      <Notice>
        Teams are not enabled on this controller. Everything here belongs to the
        one built-in team.
      </Notice>
    );
  }
  if (state.status === "operator") {
    return (
      <Notice>
        You are signed in as the deployment operator, which belongs to no team.
        Sign in with Google or GitHub to manage a team.
      </Notice>
    );
  }
  if (state.status === "unavailable") {
    return (
      <Notice>
        The controller did not return your account. Reload, or sign in again.
      </Notice>
    );
  }
  const { me, caps } = state;
  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-4xl mx-auto w-full">
      <InvitationsForMe me={me} />
      <TeamHeader me={me} />
      <div className="flex gap-1 border-b border-[var(--border)] mb-6">
        {teamTabs(caps).map((t) => {
          const active =
            t.href === "/team"
              ? pathname === "/team"
              : pathname.startsWith(t.href);
          return (
            <Link
              key={t.href}
              href={t.href}
              className={`px-3 py-2 text-sm border-b-2 -mb-px ${
                active
                  ? "border-[var(--accent)] text-[var(--foreground)]"
                  : "border-transparent text-[var(--muted)] hover:text-[var(--foreground)]"
              }`}
            >
              {t.label}
            </Link>
          );
        })}
      </div>
      {children(me, caps)}
    </div>
  );
}

function TeamHeader({ me }: { me: Me }) {
  const team = me.active_team;
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState(team.display_name);
  const [saving, setSaving] = useState(false);

  async function save() {
    const next = name.trim();
    if (!next || next === team.display_name) {
      setEditing(false);
      return;
    }
    setSaving(true);
    try {
      await renameTeam(next);
      await refreshTeamState();
      setEditing(false);
      toast("Team renamed", "success");
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="flex items-start justify-between gap-4 mb-4">
      <div className="min-w-0">
        {editing ? (
          <form
            className="flex items-center gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              save();
            }}
          >
            <input
              autoFocus
              aria-label="Team name"
              className={`${inputClass} text-lg w-72`}
              value={name}
              maxLength={80}
              onChange={(e) => setName(e.target.value)}
            />
            <button type="submit" className={buttonClass} disabled={saving}>
              {saving ? "Saving…" : "Save"}
            </button>
            <button
              type="button"
              className={quietButtonClass}
              onClick={() => {
                setName(team.display_name);
                setEditing(false);
              }}
            >
              Cancel
            </button>
          </form>
        ) : (
          <div className="flex items-center gap-3">
            <h1 className="text-xl font-bold truncate">{team.display_name}</h1>
            <RolePill role={team.role} />
            {team.role === "owner" ? (
              <button
                type="button"
                className={quietButtonClass}
                onClick={() => {
                  setName(team.display_name);
                  setEditing(true);
                }}
              >
                Rename
              </button>
            ) : null}
          </div>
        )}
        <div className="text-xs font-mono text-[var(--muted)] mt-1">
          {team.slug}
        </div>
      </div>
      <Link href="/team/new" className={quietButtonClass}>
        New team
      </Link>
    </div>
  );
}
