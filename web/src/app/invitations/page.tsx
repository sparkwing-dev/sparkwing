"use client";

import Link from "next/link";
import { InvitationsForMe, Notice } from "@/components/TeamShell";
import { useTeamState } from "@/lib/useTeam";

// An invitation link lands here after sign-in; accepting needs the invitee's own
// session, so the page lists what /me says is waiting rather than trusting the URL.
export default function InvitationsPage() {
  const state = useTeamState();
  if (state.status === "loading") return <Notice>Loading…</Notice>;
  if (state.status !== "ready") {
    return <Notice>Teams are not enabled on this controller.</Notice>;
  }
  const { me } = state;
  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-2xl mx-auto w-full">
      <h1 className="text-xl font-bold mb-4">Invitations</h1>
      {me.invitations.length === 0 ? (
        <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-5 text-sm text-[var(--muted)]">
          Nothing is waiting for {me.user.email}. An invitation shows here only
          when you are signed in with the address it was sent to.{" "}
          <Link href="/team" className="text-[var(--accent)] hover:underline">
            Go to {me.active_team.display_name}
          </Link>
        </div>
      ) : (
        <InvitationsForMe me={me} />
      )}
    </div>
  );
}
