"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import TeamShell, {
  Panel,
  buttonClass,
  dangerButtonClass,
  errorText,
  inputClass,
  quietButtonClass,
} from "@/components/TeamShell";
import CopyField from "@/components/CopyField";
import { toast } from "@/components/Toasts";
import { githubAppEnabled } from "@/lib/githubApp";
import {
  type GitCredential,
  type GitCredentialDraft,
  type GitCredentialRelease,
  type StoredGitCredential,
  confirmGitCredential,
  defaultHTTPSUsername,
  deleteGitCredential,
  draftBody,
  draftProblem,
  emptyDraft,
  existingCredential,
  isGitHubHost,
  listGitCredentialReleases,
  listGitCredentials,
  needsConfirmation,
  normalizeHost,
  putGitCredential,
  recipientsHelp,
  sortCredentials,
  statusLabel,
  updateDraft,
} from "@/lib/gitCredentials";
import {
  type Capabilities,
  type Me,
  canManageTeam,
  unixSecondsISO,
} from "@/lib/teams";
import { fmtDateTime } from "@/lib/timeFormat";

export default function GitCredentialsPage() {
  return (
    <TeamShell>
      {(me, caps) => <GitCredentials me={me} caps={caps} />}
    </TeamShell>
  );
}

const labelClass =
  "block text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1";
const thClass =
  "px-4 py-2 text-left text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]";
const tdClass = "px-4 py-2";

function GitHubAppNote({ className = "" }: { className?: string }) {
  return (
    <p className={`text-xs text-[var(--muted)] ${className}`}>
      For github.com, prefer the{" "}
      <Link
        href="/team/github"
        className="text-[var(--accent)] hover:underline"
      >
        GitHub App
      </Link>
      : its token reads one repository for at most an hour, and it wins over a
      stored github.com credential whenever it covers the repository.
    </p>
  );
}

function detailLine(row: GitCredential): string {
  const when = row.updated_at
    ? fmtDateTime(unixSecondsISO(row.updated_at))
    : "";
  return [
    row.kind === "https" ? `user ${row.username || defaultHTTPSUsername}` : "",
    when ? `updated ${when}` : "",
    row.created_by ? `by ${row.created_by}` : "",
  ]
    .filter(Boolean)
    .join(" · ");
}

function GitCredentials({ me, caps }: { me: Me; caps: Capabilities | null }) {
  const owner = canManageTeam(me.active_team.role);
  const appEnabled = githubAppEnabled(caps);
  const [rows, setRows] = useState<GitCredential[] | null>(null);
  const [loadError, setLoadError] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [stored, setStored] = useState<StoredGitCredential | null>(null);

  const load = useCallback(async () => {
    try {
      setRows(sortCredentials(await listGitCredentials()));
      setLoadError("");
    } catch (err) {
      setLoadError(errorText(err));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function onStored(cred: StoredGitCredential) {
    setStored(cred.public_key ? cred : null);
    await load();
  }

  async function remove(row: GitCredential) {
    if (
      !window.confirm(
        `Delete the credential for ${row.host}? Runs that clone from ${row.host} fail from now on unless another credential covers them.`,
      )
    ) {
      return;
    }
    setBusy(row.host);
    try {
      await deleteGitCredential(row.host);
      if (stored?.host === row.host) setStored(null);
      toast(`Deleted the credential for ${row.host}`, "success");
      await load();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setBusy(null);
    }
  }

  async function confirm(row: GitCredential) {
    if (!row.fingerprint) return;
    setBusy(`confirm:${row.host}`);
    try {
      await confirmGitCredential(row.host, row.fingerprint);
      toast(`Confirmed the host key of ${row.host}`, "success");
      await load();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setBusy(null);
    }
  }

  return (
    <>
      <div className="mb-6 space-y-2 text-sm text-[var(--muted)]">
        <p>
          Runs clone private repositories with the credential stored for the
          repository&apos;s host. {recipientsHelp} Prefer a read-only deploy key
          scoped to one repository.
        </p>
        {appEnabled ? <GitHubAppNote className="text-sm" /> : null}
        {owner ? null : <p>Owners add, replace and confirm credentials.</p>}
      </div>
      {owner ? (
        <Panel
          title="Add or replace a credential"
          hint="One credential per host. Saving for a host that has one replaces it."
        >
          <CredentialForm
            idPrefix="new"
            initial={emptyDraft}
            rows={rows ?? []}
            appEnabled={appEnabled}
            onStored={onStored}
          />
          {stored?.public_key ? (
            <PublicKey cred={stored} onDismiss={() => setStored(null)} />
          ) : null}
        </Panel>
      ) : null}
      <Panel
        title="Credentials"
        hint="Write-only. A stored key or token is never shown again, to anyone."
      >
        {loadError ? (
          <div className="p-4 text-sm text-red-300">{loadError}</div>
        ) : rows === null ? (
          <div className="p-4 text-xs text-[var(--muted)]">Loading…</div>
        ) : rows.length === 0 ? (
          <div className="p-4 text-xs text-[var(--muted)]">
            No git credentials yet.
          </div>
        ) : (
          <ul>
            {rows.map((row) => (
              <CredentialRow
                key={row.host}
                row={row}
                owner={owner}
                appEnabled={appEnabled}
                disabled={busy !== null}
                confirming={busy === `confirm:${row.host}`}
                onDelete={() => remove(row)}
                onConfirm={() => confirm(row)}
                onStored={onStored}
              />
            ))}
          </ul>
        )}
      </Panel>
      {owner ? <Releases /> : null}
    </>
  );
}

function PublicKey({
  cred,
  onDismiss,
}: {
  cred: StoredGitCredential;
  onDismiss: () => void;
}) {
  return (
    <div className="px-4 pb-4 space-y-2">
      <div className="rounded-[var(--radius-control)] border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
        Add this public key to the repository on {cred.host} as a read-only
        deploy key, then confirm the host key below.
      </div>
      <div>
        <div className={labelClass}>Public key for {cred.host}</div>
        <CopyField value={cred.public_key ?? ""} label="Public key" multiline />
      </div>
      <button type="button" className={quietButtonClass} onClick={onDismiss}>
        Done
      </button>
    </div>
  );
}

function CredentialRow({
  row,
  owner,
  appEnabled,
  disabled,
  confirming,
  onDelete,
  onConfirm,
  onStored,
}: {
  row: GitCredential;
  owner: boolean;
  appEnabled: boolean;
  disabled: boolean;
  confirming: boolean;
  onDelete: () => void;
  onConfirm: () => void;
  onStored: (cred: StoredGitCredential) => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const pending = needsConfirmation(row);

  return (
    <li className="px-4 py-3 border-b border-[var(--border)] last:border-0">
      <div className="flex items-center gap-3">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-sm font-mono truncate">{row.host}</span>
            <span className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
              {row.kind}
            </span>
            <span
              className={`text-xs ${pending ? "text-amber-200" : "text-[var(--muted)]"}`}
            >
              {statusLabel(row)}
            </span>
          </div>
          {row.kind === "ssh" && row.fingerprint ? (
            <div className="text-xs font-mono text-[var(--muted)] truncate">
              {row.fingerprint}
            </div>
          ) : null}
          <div className="text-xs text-[var(--muted)] truncate">
            {detailLine(row)}
          </div>
        </div>
        {owner && !editing ? (
          <>
            <button
              type="button"
              className={quietButtonClass}
              disabled={disabled}
              onClick={() => setEditing(true)}
            >
              Update
            </button>
            <button
              type="button"
              className={dangerButtonClass}
              disabled={disabled}
              onClick={onDelete}
            >
              Delete
            </button>
          </>
        ) : null}
      </div>
      {pending ? (
        owner ? (
          <div className="mt-3 space-y-2 rounded-[var(--radius-control)] border border-amber-500/40 bg-amber-500/10 p-3">
            <div className="text-xs text-amber-200">
              Runs get this key only after you confirm the host key the
              controller read from {row.host}. Check the fingerprint against the
              one {row.host} publishes for its SSH host keys.
            </div>
            <div>
              <div className={labelClass}>Fingerprint</div>
              <div className="text-sm font-mono break-all">
                {row.fingerprint}
              </div>
            </div>
            {row.host_key ? (
              <div>
                <div className={labelClass}>Host key</div>
                <pre className="text-xs font-mono whitespace-pre-wrap break-all">
                  {row.host_key}
                </pre>
              </div>
            ) : null}
            <button
              type="button"
              className={buttonClass}
              disabled={disabled || !row.fingerprint}
              onClick={onConfirm}
            >
              {confirming ? "Confirming…" : "Confirm host key"}
            </button>
          </div>
        ) : (
          <div className="mt-1 text-xs text-amber-200">
            Waiting for an owner to confirm the host key. Runs do not get this
            key until then.
          </div>
        )
      ) : null}
      {editing ? (
        <div className="mt-2 rounded-[var(--radius-control)] border border-[var(--border)]">
          <CredentialForm
            idPrefix={`update-${row.host}`}
            initial={updateDraft(row)}
            fixedHost
            appEnabled={appEnabled}
            onStored={async (cred) => {
              setEditing(false);
              await onStored(cred);
            }}
            onCancel={() => setEditing(false)}
          />
        </div>
      ) : null}
    </li>
  );
}

function CredentialForm({
  idPrefix,
  initial,
  rows = [],
  fixedHost = false,
  appEnabled,
  onStored,
  onCancel,
}: {
  idPrefix: string;
  initial: GitCredentialDraft;
  rows?: GitCredential[];
  fixedHost?: boolean;
  appEnabled: boolean;
  onStored: (cred: StoredGitCredential) => Promise<void>;
  onCancel?: () => void;
}) {
  const [draft, setDraft] = useState<GitCredentialDraft>(initial);
  const [touched, setTouched] = useState(false);
  const [saving, setSaving] = useState(false);

  const problem = draftProblem(draft);
  const replaces =
    !fixedHost && draft.host ? existingCredential(rows, draft.host) : undefined;
  const set = (patch: Partial<GitCredentialDraft>) =>
    setDraft((d) => ({ ...d, ...patch }));
  const id = (name: string) => `${idPrefix}-${name}`;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setTouched(true);
    if (problem) return;
    setSaving(true);
    try {
      const cred = await putGitCredential(draftBody(draft));
      toast(`Saved the credential for ${cred.host}`, "success");
      setDraft(
        fixedHost ? updateDraft(cred) : { ...emptyDraft, kind: draft.kind },
      );
      setTouched(false);
      await onStored(cred);
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setSaving(false);
    }
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-3 p-4" noValidate>
      {fixedHost ? null : (
        <div>
          <label htmlFor={id("host")} className={labelClass}>
            Host
          </label>
          <input
            id={id("host")}
            autoComplete="off"
            spellCheck={false}
            maxLength={253}
            placeholder="gitlab.com"
            className={`${inputClass} w-full font-mono`}
            value={draft.host}
            onChange={(e) => set({ host: e.target.value })}
          />
          {appEnabled && isGitHubHost(draft.host) ? (
            <GitHubAppNote className="mt-1" />
          ) : null}
        </div>
      )}
      <fieldset className="flex flex-wrap gap-4 text-sm">
        <legend className={labelClass}>Kind</legend>
        <label className="flex items-center gap-1.5">
          <input
            type="radio"
            name={id("kind")}
            checked={draft.kind === "ssh"}
            onChange={() => set({ kind: "ssh" })}
          />
          SSH deploy key
        </label>
        <label className="flex items-center gap-1.5">
          <input
            type="radio"
            name={id("kind")}
            checked={draft.kind === "https"}
            onChange={() => set({ kind: "https" })}
          />
          HTTPS token
        </label>
      </fieldset>
      {draft.kind === "ssh" ? (
        <>
          <div>
            <label htmlFor={id("key")} className={labelClass}>
              Private key{fixedHost ? ` for ${initial.host}` : ""}
            </label>
            <textarea
              id={id("key")}
              autoFocus={fixedHost}
              rows={5}
              autoComplete="off"
              spellCheck={false}
              placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
              className={`${inputClass} w-full font-mono`}
              value={draft.privateKey}
              onChange={(e) => set({ privateKey: e.target.value })}
            />
            <p className="text-xs text-[var(--muted)] mt-1">
              An unencrypted key. The controller reads the host key from the
              host when you save, and you confirm it before runs get the key.
            </p>
          </div>
          <div>
            <label htmlFor={id("port")} className={labelClass}>
              SSH port (optional)
            </label>
            <input
              id={id("port")}
              inputMode="numeric"
              autoComplete="off"
              placeholder="22"
              className={`${inputClass} w-28 font-mono`}
              value={draft.port}
              onChange={(e) => set({ port: e.target.value })}
            />
          </div>
        </>
      ) : (
        <>
          <div>
            <label htmlFor={id("token")} className={labelClass}>
              Token{fixedHost ? ` for ${initial.host}` : ""}
            </label>
            <input
              id={id("token")}
              type="password"
              autoFocus={fixedHost}
              autoComplete="off"
              spellCheck={false}
              maxLength={1024}
              className={`${inputClass} w-full font-mono`}
              value={draft.token}
              onChange={(e) => set({ token: e.target.value })}
            />
          </div>
          <div>
            <label htmlFor={id("username")} className={labelClass}>
              Username (optional)
            </label>
            <input
              id={id("username")}
              autoComplete="off"
              spellCheck={false}
              maxLength={128}
              placeholder={defaultHTTPSUsername}
              className={`${inputClass} w-full font-mono`}
              value={draft.username}
              onChange={(e) => set({ username: e.target.value })}
            />
            <p className="text-xs text-[var(--muted)] mt-1">
              Defaults to{" "}
              <span className="font-mono">{defaultHTTPSUsername}</span>.
              Bitbucket repository tokens need{" "}
              <span className="font-mono">x-token-auth</span>.
            </p>
          </div>
        </>
      )}
      {touched && problem ? (
        <div role="alert" className="text-xs text-red-300">
          {problem}
        </div>
      ) : null}
      {replaces ? (
        <div className="text-xs text-amber-200">
          Saving replaces the {replaces.kind} credential for{" "}
          {normalizeHost(draft.host)}.
        </div>
      ) : null}
      <div className="flex gap-2">
        <button type="submit" className={buttonClass} disabled={saving}>
          {saving
            ? "Saving…"
            : fixedHost
              ? "Replace credential"
              : "Save credential"}
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

function Releases() {
  const [rows, setRows] = useState<GitCredentialRelease[] | null>(null);
  const [loadError, setLoadError] = useState("");

  useEffect(() => {
    let live = true;
    listGitCredentialReleases()
      .then((r) => {
        if (live) setRows(r);
      })
      .catch((err) => {
        if (live) setLoadError(errorText(err));
      });
    return () => {
      live = false;
    };
  }, []);

  return (
    <Panel
      title="Recent releases"
      hint="Each time a runner received a credential for a run, newest first."
    >
      {loadError ? (
        <div className="p-4 text-sm text-red-300">{loadError}</div>
      ) : rows === null ? (
        <div className="p-4 text-xs text-[var(--muted)]">Loading…</div>
      ) : rows.length === 0 ? (
        <div className="p-4 text-xs text-[var(--muted)]">
          No credential has been released yet.
        </div>
      ) : (
        <table className="w-full text-sm">
          <thead className="border-b border-[var(--border)]">
            <tr>
              <th scope="col" className={thClass}>
                Host
              </th>
              <th scope="col" className={thClass}>
                Run
              </th>
              <th scope="col" className={thClass}>
                Runner
              </th>
              <th scope="col" className={`${thClass} text-right`}>
                When
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r, i) => (
              <tr
                key={`${r.run_id}-${r.host}-${r.released_at}-${i}`}
                className="border-b border-[var(--border)] last:border-0"
              >
                <td className={`${tdClass} font-mono`}>{r.host}</td>
                <td className={`${tdClass} font-mono truncate max-w-[14rem]`}>
                  {r.run_id ? (
                    <Link
                      href={`/runs?run=${encodeURIComponent(r.run_id)}`}
                      className="text-[var(--accent)] hover:underline"
                    >
                      {r.run_id}
                    </Link>
                  ) : null}
                </td>
                <td className={`${tdClass} truncate max-w-[14rem]`}>
                  {r.runner}
                  {r.token_prefix ? (
                    <span className="ml-1 text-xs font-mono text-[var(--muted)]">
                      {r.token_prefix}
                    </span>
                  ) : null}
                </td>
                <td className={`${tdClass} text-right text-[var(--muted)]`}>
                  {fmtDateTime(unixSecondsISO(r.released_at))}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Panel>
  );
}
