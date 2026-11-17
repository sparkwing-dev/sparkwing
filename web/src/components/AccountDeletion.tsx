"use client";

import { TeamApiError } from "@/lib/teams";
import { useEffect, useRef, useState } from "react";
import { Panel, errorText, quietButtonClass, dangerButtonClass, inputClass } from "./TeamShell";
import { type AccountDeletionPreview, deleteAccount, previewAccountDeletion, signInAgainForDeletion } from "@/lib/accountDeletion";

export default function AccountDeletion() {
  const [plan, setPlan] = useState<AccountDeletionPreview | null>(null);
  const [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const opener = useRef<HTMLButtonElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const signInButton = useRef<HTMLButtonElement>(null);
  const alert = useRef<HTMLParagraphElement>(null);

  useEffect(() => {
    if (busy) return;
    if (error) alert.current?.focus();
    else if (plan?.sign_in_required) signInButton.current?.focus();
    else if (plan) input.current?.focus();
  }, [plan, busy, error]);

  async function preview() {
    setBusy(true);
    setError("");
    try {
      setPlan(await previewAccountDeletion());
      setConfirmation("");
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (!plan) return;
    setBusy(true);
    setError("");
    try {
      await deleteAccount(plan, confirmation);
      window.location.assign(new URL("/login?account_deleted=1", window.location.origin).href);
    } catch (err) {
      if (err instanceof TeamApiError && err.reason === "reauth_required") {
        setPlan({ ...plan, sign_in_required: true });
      }
      setError(`${errorText(err)}. Review the deletion again before retrying.`);
      setConfirmation("");
    } finally {
      setBusy(false);
    }
  }

  async function reauthenticate() {
    setBusy(true);
    setError("");
    try { await signInAgainForDeletion(); }
    catch (err) { setError(errorText(err)); }
    finally { setBusy(false); }
  }

  return <Panel title="Delete account">
    <div className="p-4 text-sm space-y-3">
      <p>Deleting your account ends every session and revokes every token immediately.</p>
      <p>Teams where you are the only member are deleted with their runs, logs, artifacts, secrets and other operational data. Cleanup normally finishes within minutes and retries if it fails. On Sparkwing Cloud, automated RDS backups have a seven-day recovery window. Older accounts can remain in encrypted operator archives beyond that window. Self-hosted backup retention follows the operator&apos;s policy.</p>
      <p>Shared-team runs, logs and artifacts stay with the team. Selected execution actor fields are relabeled as deleted, while some creator references retain your opaque account ID. Their contents may still name you. Financial records remain for accounting. Audit identifiers remain for security and abuse investigation, for 400 days by default. OIDC audit rows also retain their audience and subject as plain strings, which can include personal text; account deletion does not scrub them.</p>
      <button ref={opener} type="button" disabled={busy} onClick={preview} className={`${quietButtonClass} min-h-11 px-3`}>Review account deletion</button>
      {plan && <div className="space-y-3">
        <p>{plan.credential_notice}</p>
        <p>{plan.archive_notice} <a className="underline" href="https://sparkwing.dev/docs/account-deletion-retention" target="_blank" rel="noopener noreferrer">Retention details</a></p>
        <p>These teams and their runs and data will be deleted:</p>
        {plan.teams.length ? <ul className="list-disc pl-5">{plan.teams.map(team => <li key={team} className="font-mono break-all">{team}</li>)}</ul> : <p>No teams will be deleted.</p>}
        {plan.blocked_teams.length > 0 ? <p role="alert" className="break-all">Transfer ownership to another member or delete these teams first: {plan.blocked_teams.join(", ")}.</p> : plan.sign_in_required ? <div className="space-y-3">
          <p>Account deletion needs a sign-in from the last 10 minutes. Sign out and sign in again, then review the deletion with the new session.</p>
          <button ref={signInButton} type="button" disabled={busy} onClick={reauthenticate} className={`${quietButtonClass} min-h-11 px-3`}>Sign in again</button>
        </div> : <>
          <label className="block" htmlFor="delete-account-confirm">Type your account ID to confirm: <span className="font-mono">{plan.account_id}</span></label>
          <input ref={input} id="delete-account-confirm" autoComplete="off" value={confirmation} disabled={busy} onChange={e => setConfirmation(e.target.value)} className={`${inputClass} w-full font-mono`} />
          <div className="flex gap-4">
            <button type="button" disabled={busy || confirmation !== plan.account_id} onClick={remove} className={`${dangerButtonClass} min-h-11 px-3`}>Delete account permanently</button>
            <button type="button" disabled={busy} onClick={() => { setPlan(null); setError(""); setConfirmation(""); opener.current?.focus(); }} className={`${quietButtonClass} min-h-11 px-3`}>Cancel</button>
          </div>
        </>}
      </div>}
      {error && <p ref={alert} role="alert" tabIndex={-1} className="text-red-500">{error}</p>}
    </div>
  </Panel>;
}
