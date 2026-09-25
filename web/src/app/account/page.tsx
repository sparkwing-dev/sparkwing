"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import {
  Notice,
  Panel,
  dangerButtonClass,
  errorText,
  inputClass,
} from "@/components/TeamShell";
import { toast } from "@/components/Toasts";
import {
  type Me,
  type TeamDeletion,
  type TeamRef,
  confirmationMatches,
  deleteAccount,
  listTeamDeletions,
  unixSecondsISO,
} from "@/lib/teams";
import { fmtDateTime } from "@/lib/timeFormat";
import { useTeamState } from "@/lib/useTeam";

export default function AccountPage() {
  const state = useTeamState();
  if (state.status === "loading") return <Notice>Loading…</Notice>;
  if (state.status !== "ready") {
    return (
      <Notice>
        Accounts exist only on a controller with teams enabled, for people who
        sign in with Google or GitHub.
      </Notice>
    );
  }
  return <Account me={state.me} />;
}

function Account({ me }: { me: Me }) {
  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-2xl mx-auto w-full">
      <h1 className="text-xl font-bold mb-1">Your account</h1>
      <div className="text-sm text-[var(--muted)] mb-6">
        {me.user.name ? `${me.user.name} · ` : ""}
        {me.user.email}
      </div>
      <Panel title="Linked sign-ins">
        <div className="p-4 text-sm flex items-center gap-3">
          <p className="flex-1 text-[var(--muted)]">
            Add or remove the Google and GitHub sign-ins that reach this
            account.
          </p>
          <Link href="/account/sign-ins" className="underline">
            Manage
          </Link>
        </div>
      </Panel>
      <TeamDeletions />
      <DeleteAccount me={me} />
    </div>
  );
}

function TeamDeletions() {
  const [deletions, setDeletions] = useState<TeamDeletion[] | null>(null);
  useEffect(() => {
    listTeamDeletions()
      .then(setDeletions)
      .catch((err) => toast(errorText(err), "error"));
  }, []);
  if (!deletions || deletions.length === 0) return null;
  return (
    <Panel
      title="Team deletions"
      hint="Teams you deleted. A pending deletion finishes within a few minutes and retries on its own if a step fails."
    >
      <ul>
        {deletions.map((d) => (
          <li
            key={d.slug + d.requested_at}
            className="px-4 py-3 border-b border-[var(--border)] last:border-0 text-sm"
          >
            <div className="flex items-center gap-3">
              <span className="font-mono flex-1 truncate">{d.slug}</span>
              <span className="text-xs text-[var(--muted)]">
                {d.state === "done"
                  ? `deleted ${fmtDateTime(unixSecondsISO(d.finished_at ?? undefined))}`
                  : `deleting since ${fmtDateTime(unixSecondsISO(d.requested_at))}`}
              </span>
            </div>
            {d.state === "pending" && d.last_error ? (
              <div className="mt-1 text-xs text-amber-300">
                Retrying after {d.attempts} failed{" "}
                {d.attempts === 1 ? "attempt" : "attempts"}: {d.last_error}
              </div>
            ) : null}
          </li>
        ))}
      </ul>
    </Panel>
  );
}

function DeleteAccount({ me }: { me: Me }) {
  const [typed, setTyped] = useState("");
  const [deleting, setDeleting] = useState(false);
  const [blocked, setBlocked] = useState<TeamRef[] | null>(null);
  const [needsSignIn, setNeedsSignIn] = useState(false);
  const ready = confirmationMatches(typed, me.user.email);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!ready) return;
    setDeleting(true);
    try {
      const res = await deleteAccount(me.user.email);
      if (res.kind === "blocked") {
        setBlocked(res.teams);
        setDeleting(false);
        return;
      }
      if (res.kind === "reauth") {
        setNeedsSignIn(true);
        setDeleting(false);
        return;
      }
      // The controller ended every session, so a full load lands on sign-in.
      window.location.reload();
    } catch (err) {
      setDeleting(false);
      toast(errorText(err), "error");
    }
  }

  return (
    <Panel title="Delete account">
      <div className="p-4 space-y-3 text-sm">
        <p className="text-[var(--muted)]">
          Deleting your account signs you out everywhere and removes your
          sign-in, your membership in every team, and every runner and CLI token
          you created. Teams where you are the only member, your personal space
          included, are deleted with everything in them: runs, logs, secrets and
          cached artifacts. In teams you share, the runs, approvals, schedules
          and secrets you created stay with the team and name &ldquo;deleted
          user&rdquo; instead of you, and so do usage records. This cannot be
          undone. Signing in again later creates a new, empty account.
        </p>
        <p className="text-[var(--muted)]">
          If you are the last owner of a team that has other members, make
          someone else an owner or delete that team first. Deleting needs a
          sign-in from the last 10 minutes.
        </p>
        {needsSignIn ? (
          <div className="rounded-[var(--radius-control)] border border-amber-500/40 bg-amber-500/10 p-3 text-xs text-amber-200">
            Your account was not deleted. Log out, sign in again, and delete it
            within 10 minutes of signing in.
          </div>
        ) : null}
        {blocked && blocked.length > 0 ? (
          <div className="rounded-[var(--radius-control)] border border-amber-500/40 bg-amber-500/10 p-3 text-xs text-amber-200">
            Your account was not deleted. You are the last owner of:
            <ul className="mt-1 list-disc pl-5">
              {blocked.map((t) => (
                <li key={t.slug}>
                  {t.display_name} <span className="font-mono">({t.slug})</span>
                </li>
              ))}
            </ul>
            <div className="mt-1">
              Switch to each team and promote another member to owner on its{" "}
              <Link href="/team" className="underline">
                Members
              </Link>{" "}
              page, or delete the team there.
            </div>
          </div>
        ) : null}
        <form onSubmit={submit} className="flex flex-wrap items-center gap-2">
          <input
            aria-label="Type your email to confirm"
            placeholder={me.user.email}
            className={`${inputClass} flex-1 min-w-[14rem]`}
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
          />
          <button
            type="submit"
            className={dangerButtonClass}
            disabled={deleting || !ready}
          >
            {deleting ? "Deleting…" : "Delete my account"}
          </button>
        </form>
        <p className="text-xs text-[var(--muted)]">
          Type <span className="font-mono">{me.user.email}</span> to confirm.
        </p>
      </div>
    </Panel>
  );
}
