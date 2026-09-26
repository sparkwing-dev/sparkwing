"use client";

import Link from "next/link";
import { Notice } from "@/components/TeamShell";
import SecretsManager from "@/components/SecretsManager";
import { useTeamState } from "@/lib/useTeam";

// Outside TeamShell because a controller without teams has no account for
// /me to return; its one built-in team is the whole store.
export default function LocalSecretsPage() {
  const state = useTeamState();
  if (state.status === "loading") {
    return <Notice>Loading…</Notice>;
  }
  if (state.status !== "single-team") {
    return (
      <Notice>
        Secrets on this controller belong to a team. Manage them under{" "}
        <Link href="/team/secrets" className="underline">
          Team → Secrets
        </Link>
        .
      </Notice>
    );
  }
  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-4xl mx-auto w-full">
      <h1 className="text-xl font-bold mb-4">Secrets and variables</h1>
      <SecretsManager owner audience="local" />
    </div>
  );
}
