"use client";

import { useRef, useState } from "react";
import { Panel, buttonClass, errorText, inputClass } from "./TeamShell";
import { requestReseal, resealCounts, resealVerified, type ResealCounts } from "@/lib/reseal";

export function ReencryptSecrets() {
  const [before, setBefore] = useState<ResealCounts | null>(null);
  const [rotated, setRotated] = useState<ResealCounts | null>(null);
  const [after, setAfter] = useState<ResealCounts | null>(null);
  const [confirmation, setConfirmation] = useState("");
  const [verified, setVerified] = useState(false);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("Run a dry-run verification before re-encrypting.");
  const [error, setError] = useState("");
  const pending = useRef(false);

  async function verify() {
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    setVerified(false);
    setConfirmation("");
    setError("");
    setStatus("Verifying secrets. No values are changed.");
    setBefore(null);
    setRotated(null);
    setAfter(null);
    try {
      const counts = await requestReseal(true);
      setBefore(counts);
      if (counts.unreadable > 0) {
        setError("Unreadable rows found. Repair or remove those rows before re-encrypting, then verify again.");
        setStatus("");
      } else {
        setVerified(true);
        setStatus("Verification passed. Review the counts, then type the confirmation.");
      }
    } catch (err) {
      setError(`Verification failed. ${errorText(err)}`);
      setStatus("");
    } finally {
      pending.current = false;
      setBusy(false);
    }
  }

  async function rotate() {
    if (pending.current || !verified || confirmation !== "RE-ENCRYPT") return;
    pending.current = true;
    setBusy(true);
    setVerified(false);
    setConfirmation("");
    setError("");
    setStatus("Re-encrypting secrets. Keep this page open for the final verification.");
    let resealed = false;
    try {
      const result = await requestReseal(false);
      setRotated(result);
      resealed = true;
      setStatus("Re-encryption returned. Verifying the stored values again.");
      const final = await requestReseal(true);
      setAfter(final);
      if (resealVerified(final)) {
        setStatus("Re-encryption complete. Verification found no previous-key-only rows, plaintext argument documents or unreadable rows.");
      } else {
        setError("Verification still found rows needing attention. Review the counts and controller keys, repair unreadable rows, then verify again before retrying.");
        setStatus("");
      }
    } catch (err) {
      setError(`${resealed ? "Re-encryption returned, but final verification failed." : "Re-encryption was not confirmed. Some rows may have changed."} ${errorText(err)}`);
      setStatus("");
    } finally {
      pending.current = false;
      setBusy(false);
    }
  }

  return (
    <Panel title="Re-encrypt secrets" hint="Re-encrypt stored values under the controller's configured current key. No key material or argument values are shown.">
      <div className="p-4 space-y-4 text-sm">
        <p className="text-[var(--muted)]">Sign in within the last 10 minutes. Before continuing, confirm a current database backup and that every controller has the current and previous keys configured.</p>
        <button className={buttonClass} disabled={busy} onClick={verify}>Verify secrets</button>
        <form onSubmit={(event) => { event.preventDefault(); void rotate(); }}>
          <label htmlFor="reseal-confirmation" className="block mb-2">Type RE-ENCRYPT to confirm</label>
          <div className="flex flex-col sm:flex-row gap-3 sm:items-center">
            <input
              id="reseal-confirmation"
              className={`${inputClass} min-h-11 w-full sm:w-60`}
              value={confirmation}
              disabled={busy || !verified}
              autoComplete="off"
              spellCheck={false}
              onChange={(event) => setConfirmation(event.target.value)}
            />
            <button className={buttonClass} type="submit" disabled={busy || !verified || confirmation !== "RE-ENCRYPT"}>Re-encrypt now</button>
          </div>
        </form>
        <div aria-live="polite" aria-atomic="true">
          {error ? <p role="alert" className="text-[var(--danger)]">{error}</p> : <p role="status" className="text-[var(--muted)]">{status}</p>}
        </div>
        <table className="w-full text-xs sm:text-sm tabular-nums">
          <caption className="text-left text-[var(--muted)] mb-2">Counts across all teams. Before and After are dry runs. Re-encrypted counts describe the write operation.</caption>
          <thead>
            <tr className="text-[var(--muted)]">
              <th scope="col" className="text-left py-2">Stored values</th>
              <th scope="col" className="text-right px-1">Before</th>
              <th scope="col" className="text-right px-1">Re-encrypted</th>
              <th scope="col" className="text-right pl-1">After</th>
            </tr>
          </thead>
          <tbody>
            {resealCounts.map(([key, label]) => (
              <tr key={key} className="border-t border-[var(--border)]">
                <th scope="row" className="font-normal text-left py-2 pr-2">{label}</th>
                <td className="text-right px-1">{before?.[key] ?? "—"}</td>
                <td className="text-right px-1">{rotated?.[key] ?? "—"}</td>
                <td className="text-right pl-1">{after?.[key] ?? "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Panel>
  );
}
