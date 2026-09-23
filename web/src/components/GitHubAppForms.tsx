"use client";

import { useState } from "react";
import {
  buttonClass,
  dangerButtonClass,
  inputClass,
  quietButtonClass,
} from "@/components/TeamShell";
import {
  type GitHubAppInstallation,
  type GitHubAppRepository,
  type GitHubAppSubscription,
  type SubscriptionDraft,
  accountTypeLabel,
  connectFormAction,
  emptySubscriptionDraft,
  pullRequestHelp,
  subscriptionProblem,
} from "@/lib/githubApp";

// A native form, because the dashboard server answers it with a redirect to
// GitHub and keeps the flow in a cookie the page never sees.
export function ConnectGitHubForm({
  csrfToken,
  connected,
}: {
  csrfToken: string;
  connected: boolean;
}) {
  return (
    <form method="POST" action={connectFormAction}>
      <input type="hidden" name="csrf_token" value={csrfToken} />
      <button type="submit" className={buttonClass} disabled={!csrfToken}>
        {connected ? "Connect another account" : "Connect GitHub"}
      </button>
    </form>
  );
}

export function InstallationRow({
  inst,
  repositories,
  repositoriesError,
  canManage,
  busy,
  onDisconnect,
}: {
  inst: GitHubAppInstallation;
  repositories: GitHubAppRepository[] | undefined;
  repositoriesError?: string;
  canManage: boolean;
  busy: boolean;
  onDisconnect: (inst: GitHubAppInstallation) => void;
}) {
  return (
    <li className="px-4 py-3 border-b border-[var(--border)] last:border-0">
      <div className="flex items-center gap-3">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 text-sm">
            <span className="font-medium truncate">{inst.account_login}</span>
            <span className="text-xs text-[var(--muted)]">
              {accountTypeLabel(inst.account_type)}
            </span>
            {inst.suspended ? (
              <span
                className="text-[10px] uppercase tracking-wider px-1.5 py-0.5 rounded border border-amber-500/40 text-amber-300"
                title="GitHub suspended this installation; it starts no runs and fetches no source."
              >
                Suspended
              </span>
            ) : null}
          </div>
        </div>
        <a
          href={inst.manage_url}
          target="_blank"
          rel="noreferrer"
          className={quietButtonClass}
        >
          Manage on GitHub
        </a>
        {canManage ? (
          <button
            type="button"
            className={dangerButtonClass}
            disabled={busy}
            onClick={() => onDisconnect(inst)}
          >
            Disconnect
          </button>
        ) : null}
      </div>
      <div className="mt-2 text-xs text-[var(--muted)]">
        {repositoriesError ? (
          <span className="text-red-300">{repositoriesError}</span>
        ) : repositories === undefined ? (
          "Loading repositories…"
        ) : repositories.length === 0 ? (
          "This installation covers no repositories. Choose them on GitHub."
        ) : (
          <ul className="flex flex-wrap gap-1.5">
            {repositories.map((r) => (
              <li
                key={r.repository_id}
                className="font-mono px-1.5 py-0.5 rounded bg-[var(--background)] border border-[var(--border)]"
              >
                {r.full_name}
                {r.private ? " (private)" : ""}
              </li>
            ))}
          </ul>
        )}
      </div>
    </li>
  );
}

export function SubscriptionForm({
  repositories,
  initial = emptySubscriptionDraft,
  busy,
  onSave,
  onCancel,
}: {
  repositories: string[];
  initial?: SubscriptionDraft;
  busy: boolean;
  onSave: (draft: SubscriptionDraft) => void;
  onCancel?: () => void;
}) {
  const [draft, setDraft] = useState<SubscriptionDraft>(initial);
  const [shown, setShown] = useState<string | null>(null);
  const set = (patch: Partial<SubscriptionDraft>) =>
    setDraft((d) => ({ ...d, ...patch }));
  const choices =
    draft.repository && !repositories.includes(draft.repository)
      ? [draft.repository, ...repositories]
      : repositories;

  return (
    <form
      className="p-4 flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        const problem = subscriptionProblem(draft);
        setShown(problem);
        if (!problem) onSave(draft);
      }}
    >
      <div className="flex flex-wrap gap-3">
        <label className="flex flex-col gap-1 text-xs text-[var(--muted)]">
          Repository
          <select
            className={`${inputClass} w-72`}
            value={draft.repository}
            onChange={(e) => set({ repository: e.target.value })}
          >
            <option value="">Choose a repository</option>
            {choices.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-xs text-[var(--muted)]">
          Pipeline
          <input
            className={`${inputClass} w-56 font-mono`}
            placeholder="ci"
            value={draft.pipeline}
            onChange={(e) => set({ pipeline: e.target.value })}
          />
        </label>
      </div>
      <fieldset className="flex flex-wrap gap-4 text-sm">
        <legend className="sr-only">Run on</legend>
        <label className="flex items-center gap-2">
          <input
            type="checkbox"
            checked={draft.push}
            onChange={(e) => set({ push: e.target.checked })}
          />
          Pushes
        </label>
        <label className="flex items-center gap-2">
          <input
            type="checkbox"
            checked={draft.pull_request}
            onChange={(e) => set({ pull_request: e.target.checked })}
          />
          Pull requests
        </label>
      </fieldset>
      <p role="note" className="text-xs text-[var(--muted)]">
        {pullRequestHelp}
      </p>
      <label className="flex flex-col gap-1 text-xs text-[var(--muted)]">
        Push branches (one pattern per line; empty runs on every branch)
        <textarea
          className={`${inputClass} w-full font-mono`}
          rows={2}
          value={(draft.branches ?? []).join("\n")}
          onChange={(e) => set({ branches: e.target.value.split("\n") })}
          placeholder={"main\nrelease/*"}
        />
      </label>
      <label className="flex flex-col gap-1 text-xs text-[var(--muted)]">
        Pull request base branches (one pattern per line; empty runs on every base branch)
        <textarea
          className={`${inputClass} w-full font-mono`}
          rows={2}
          value={(draft.base_branches ?? []).join("\n")}
          onChange={(e) => set({ base_branches: e.target.value.split("\n") })}
          placeholder="main"
        />
      </label>
      {shown ? (
        <div role="alert" className="text-xs text-red-300">
          {shown}
        </div>
      ) : null}
      <div className="flex gap-2">
        <button type="submit" className={buttonClass} disabled={busy}>
          {busy ? "Saving…" : "Save subscription"}
        </button>
        {onCancel ? (
          <button type="button" className={quietButtonClass} onClick={onCancel}>
            Cancel
          </button>
        ) : null}
      </div>
    </form>
  );
}

function Mark({ on, label }: { on: boolean; label: string }) {
  return (
    <span
      aria-label={`${label}: ${on ? "yes" : "no"}`}
      className={on ? "text-[var(--foreground)]" : "text-[var(--muted)]"}
    >
      {on ? "Yes" : "No"}
    </span>
  );
}

export function SubscriptionsTable({
  subscriptions,
  canManage,
  busy,
  onEdit,
  onRemove,
}: {
  subscriptions: GitHubAppSubscription[];
  canManage: boolean;
  busy: boolean;
  onEdit: (sub: GitHubAppSubscription) => void;
  onRemove: (sub: GitHubAppSubscription) => void;
}) {
  if (subscriptions.length === 0) {
    return (
      <div className="p-4 text-xs text-[var(--muted)]">
        No pipeline runs from GitHub yet.
      </div>
    );
  }
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="text-left text-xs text-[var(--muted)] border-b border-[var(--border)]">
          <th className="px-4 py-2 font-medium">Repository</th>
          <th className="px-4 py-2 font-medium">Pipeline</th>
          <th className="px-4 py-2 font-medium">Pushes</th>
          <th className="px-4 py-2 font-medium">Push branches</th>
          <th className="px-4 py-2 font-medium">Pull requests</th>
          <th className="px-4 py-2 font-medium">Base branches</th>
          {canManage ? <th className="px-4 py-2" /> : null}
        </tr>
      </thead>
      <tbody>
        {subscriptions.map((s) => (
          <tr
            key={`${s.repository_id}/${s.pipeline}`}
            className="border-b border-[var(--border)] last:border-0"
          >
            <td className="px-4 py-2 font-mono">{s.repository}</td>
            <td className="px-4 py-2 font-mono">{s.pipeline}</td>
            <td className="px-4 py-2">
              <Mark on={s.push} label="Pushes" />
            </td>
            <td className="px-4 py-2 font-mono text-xs">{s.branches?.join(", ") || "All"}</td>
            <td className="px-4 py-2">
              <Mark on={s.pull_request} label="Pull requests" />
            </td>
            <td className="px-4 py-2 font-mono text-xs">{s.base_branches?.join(", ") || "All"}</td>
            {canManage ? (
              <td className="px-4 py-2 text-right whitespace-nowrap">
                <button
                  type="button"
                  className={`${quietButtonClass} mr-2`}
                  disabled={busy}
                  onClick={() => onEdit(s)}
                >
                  Edit
                </button>
                <button
                  type="button"
                  className={dangerButtonClass}
                  disabled={busy}
                  onClick={() => onRemove(s)}
                >
                  Remove
                </button>
              </td>
            ) : null}
          </tr>
        ))}
      </tbody>
    </table>
  );
}
