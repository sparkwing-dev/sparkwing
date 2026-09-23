"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import {
  Notice,
  buttonClass,
  errorText,
  inputClass,
  quietButtonClass,
} from "@/components/TeamShell";
import { toast } from "@/components/Toasts";
import { createTeam, slugFromName, teamSlugProblem } from "@/lib/teams";
import { createdNotice } from "@/lib/teamNotice";
import { refreshTeamState, useTeamState } from "@/lib/useTeam";

export default function NewTeamPage() {
  const state = useTeamState();
  const router = useRouter();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugEdited, setSlugEdited] = useState(false);
  const [creating, setCreating] = useState(false);

  if (state.status === "loading") return <Notice>Loading…</Notice>;
  if (state.status !== "ready") {
    return (
      <Notice>
        Teams are not enabled on this controller, so it runs one built-in team.
      </Notice>
    );
  }

  const effectiveSlug = slugEdited ? slug : slugFromName(name);
  const problem = effectiveSlug ? teamSlugProblem(effectiveSlug) : null;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim() || !effectiveSlug || problem) return;
    setCreating(true);
    try {
      await createTeam(effectiveSlug, name.trim());
      await refreshTeamState();
      toast(createdNotice(name.trim()), "success");
      router.push("/team");
    } catch (err) {
      setCreating(false);
      toast(errorText(err), "error");
    }
  }

  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-xl mx-auto w-full">
      <h1 className="text-xl font-bold mb-1">Create a team</h1>
      <p className="text-sm text-[var(--muted)] mb-6">
        You&apos;ll own the new team and switch to it. Your personal space and
        other teams stay as they are - switch between them anytime from the team
        menu. Each team has its own pipelines, runs, secrets and machines.
      </p>
      <form
        onSubmit={submit}
        className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-5 space-y-4"
      >
        <label className="block">
          <span className="block text-xs text-[var(--muted)] mb-1">Name</span>
          <input
            required
            autoFocus
            maxLength={80}
            placeholder="Acme Platform"
            className={`${inputClass} w-full`}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
        <label className="block">
          <span className="block text-xs text-[var(--muted)] mb-1">
            Slug{" "}
            <span className="text-[10px]">
              (permanent; used in URLs and hostnames)
            </span>
          </span>
          <input
            required
            maxLength={40}
            placeholder="acme-platform"
            className={`${inputClass} w-full font-mono`}
            value={effectiveSlug}
            onChange={(e) => {
              setSlugEdited(true);
              setSlug(e.target.value.toLowerCase());
            }}
          />
          {problem ? (
            <span className="block text-xs text-red-300 mt-1">{problem}</span>
          ) : null}
        </label>
        <div className="flex items-center gap-2 pt-1">
          <button
            type="submit"
            className={buttonClass}
            disabled={creating || !name.trim() || !effectiveSlug || !!problem}
          >
            {creating ? "Creating…" : "Create team"}
          </button>
          <Link href="/team" className={quietButtonClass}>
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}
