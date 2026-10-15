"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import {
  Notice,
  Panel,
  errorText,
} from "@/components/TeamShell";
import { toast } from "@/components/Toasts";
import {
  type Me,
  type TeamDeletion,
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
      <p className="mt-4 text-sm text-[var(--muted)]">
        Contact the deployment operator to delete your account.
      </p>
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
