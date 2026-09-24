"use client";

import { useCallback, useEffect, useState } from "react";
import {
  Panel,
  buttonClass,
  dangerButtonClass,
  errorText,
  inputClass,
  quietButtonClass,
} from "@/components/TeamShell";
import Tooltip from "@/components/Tooltip";
import { toast } from "@/components/Toasts";
import {
  type ExtraRepos,
  extraRepoChoices,
  extraReposProblem,
  listExtraRepos,
  maxExtraRepos,
  putExtraRepos,
} from "@/lib/githubApp";

const extraReposHint =
  "Add private submodules or dependencies here, up to 10 repositories from the same owner. The GitHub installation must cover each one. Only a team owner can edit this list.";

// The team owner's per-repository list of further repositories a run's
// token reads. repositories are those the team's installations cover.
export function ExtraReposPanel({
  repositories,
  canManage,
}: {
  repositories: string[];
  canManage: boolean;
}) {
  const [lists, setLists] = useState<ExtraRepos[] | null>(null);
  const [loadError, setLoadError] = useState("");
  const [editing, setEditing] = useState<ExtraRepos | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      setLists(await listExtraRepos());
      setLoadError("");
    } catch (err) {
      setLoadError(errorText(err));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function save(draft: ExtraRepos) {
    setBusy(true);
    try {
      const saved = await putExtraRepos(draft);
      toast(
        saved.extra_repos.length
          ? `${saved.repository} also reads ${saved.extra_repos.length} repositories`
          : `${saved.repository} reads only itself`,
        "success",
      );
      setEditing(null);
      await load();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Panel
      title="Extra repositories"
      action={
        <Tooltip content={extraReposHint}>
          <button
            type="button"
            aria-label={`About extra repositories: ${extraReposHint}`}
            className={`${quietButtonClass} min-h-11 min-w-11 focus-visible:ring-2 focus-visible:ring-[var(--accent)]`}
            onClick={(event) => event.currentTarget.focus()}
          >
            Info
          </button>
        </Tooltip>
      }
    >
      {loadError ? (
        <div className="p-4 text-xs text-red-300">{loadError}</div>
      ) : lists === null ? (
        <div className="p-4 text-xs text-[var(--muted)]">Loading…</div>
      ) : (
        <ExtraReposTable
          lists={lists}
          canManage={canManage}
          busy={busy}
          onEdit={setEditing}
          onClear={(l) => save({ repository: l.repository, extra_repos: [] })}
        />
      )}
      {canManage ? (
        <div className="p-4 border-t border-[var(--border)]">
          <ExtraReposForm
            key={editing ? editing.repository : "new"}
            repositories={repositories}
            initial={editing ?? undefined}
            busy={busy}
            onSave={save}
            onCancel={editing ? () => setEditing(null) : undefined}
          />
        </div>
      ) : null}
    </Panel>
  );
}

export function ExtraReposTable({
  lists,
  canManage,
  busy,
  onEdit,
  onClear,
}: {
  lists: ExtraRepos[];
  canManage: boolean;
  busy: boolean;
  onEdit: (l: ExtraRepos) => void;
  onClear: (l: ExtraRepos) => void;
}) {
  if (lists.length === 0) {
    return (
      <div className="p-4 text-xs text-[var(--muted)]">
        Every run&apos;s token reads only its own repository.
      </div>
    );
  }
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="text-left text-xs text-[var(--muted)] border-b border-[var(--border)]">
          <th className="px-4 py-2 font-medium">Repository</th>
          <th className="px-4 py-2 font-medium">Also reads</th>
          {canManage ? <th className="px-4 py-2" /> : null}
        </tr>
      </thead>
      <tbody>
        {lists.map((l) => (
          <tr
            key={l.repository}
            className="border-b border-[var(--border)] last:border-0"
          >
            <td className="px-4 py-2 font-mono">{l.repository}</td>
            <td className="px-4 py-2 font-mono">{l.extra_repos.join(", ")}</td>
            {canManage ? (
              <td className="px-4 py-2 text-right whitespace-nowrap">
                <button
                  type="button"
                  className={`${quietButtonClass} mr-2`}
                  disabled={busy}
                  onClick={() => onEdit(l)}
                >
                  Edit
                </button>
                <button
                  type="button"
                  className={dangerButtonClass}
                  disabled={busy}
                  onClick={() => onClear(l)}
                >
                  Clear
                </button>
              </td>
            ) : null}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export function ExtraReposForm({
  repositories,
  initial,
  busy,
  onSave,
  onCancel,
}: {
  repositories: string[];
  initial?: ExtraRepos;
  busy: boolean;
  onSave: (draft: ExtraRepos) => void;
  onCancel?: () => void;
}) {
  const [draft, setDraft] = useState<ExtraRepos>(
    initial ?? { repository: "", extra_repos: [] },
  );
  const [problem, setProblem] = useState<string | null>(null);
  const choices = draft.repository
    ? extraRepoChoices(draft.repository, repositories)
    : [];

  function toggle(repo: string, on: boolean) {
    const key = repo.toLowerCase();
    const rest = draft.extra_repos.filter((r) => r.toLowerCase() !== key);
    setDraft({ ...draft, extra_repos: on ? [...rest, key] : rest });
  }

  return (
    <form
      className="space-y-3"
      onSubmit={(e) => {
        e.preventDefault();
        const p = extraReposProblem(draft);
        setProblem(p);
        if (!p) onSave(draft);
      }}
    >
      <label className="block text-xs">
        Repository
        <select
          className={inputClass}
          value={draft.repository}
          disabled={Boolean(initial)}
          onChange={(e) =>
            setDraft({ repository: e.target.value, extra_repos: [] })
          }
        >
          <option value="">Choose a repository</option>
          {repositories.map((r) => (
            <option key={r} value={r}>
              {r}
            </option>
          ))}
        </select>
      </label>
      {draft.repository ? (
        <fieldset className="text-xs space-y-1">
          <legend className="mb-1">Also reads (at most {maxExtraRepos})</legend>
          {choices.length === 0 ? (
            <div className="text-[var(--muted)]">
              No other repository of this owner is covered.
            </div>
          ) : (
            choices.map((r) => (
              <label key={r} className="flex items-center gap-2 font-mono">
                <input
                  type="checkbox"
                  checked={draft.extra_repos.includes(r.toLowerCase())}
                  onChange={(e) => toggle(r, e.target.checked)}
                />
                {r}
              </label>
            ))
          )}
        </fieldset>
      ) : null}
      {problem ? (
        <div role="alert" className="text-xs text-red-300">
          {problem}
        </div>
      ) : null}
      <div className="flex gap-2">
        <button type="submit" className={buttonClass} disabled={busy}>
          {busy ? "Saving…" : "Save extra repositories"}
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
