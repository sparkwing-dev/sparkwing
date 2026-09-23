"use client";

import { useCallback, useEffect, useState } from "react";
import TeamShell, {
  Panel,
  buttonClass,
  dangerButtonClass,
  errorText,
  inputClass,
} from "@/components/TeamShell";
import CopyField from "@/components/CopyField";
import { toast } from "@/components/Toasts";
import {
  type Me,
  type MintedRunnerToken,
  type RunnerToken,
  canConnectMachines,
  controllerURLPlaceholder,
  listRunnerTokens,
  mintRunnerToken,
  revokeRunnerToken,
  runnerConnectCommand,
  unixSecondsISO,
} from "@/lib/teams";
import { fmtDateTime } from "@/lib/timeFormat";

export default function MachinesPage() {
  return <TeamShell>{(me) => <Machines me={me} />}</TeamShell>;
}

function Machines({ me }: { me: Me }) {
  const role = me.active_team.role;
  const [tokens, setTokens] = useState<RunnerToken[] | null>(null);
  const [loadError, setLoadError] = useState("");
  const [busy, setBusy] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setTokens(await listRunnerTokens());
      setLoadError("");
    } catch (err) {
      setLoadError(errorText(err));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function revoke(t: RunnerToken) {
    const label = t.name || t.prefix;
    if (
      !window.confirm(
        `Revoke ${label}? A machine using this token stops taking work at once.`,
      )
    ) {
      return;
    }
    setBusy(t.prefix);
    try {
      await revokeRunnerToken(t.prefix);
      toast(`Revoked ${label}`, "success");
      await load();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setBusy(null);
    }
  }

  // An editor may revoke only the tokens they minted; the controller decides,
  // so the button is offered to editors and a refusal is shown as it comes back.
  const mayRevoke = canConnectMachines(role);

  return (
    <>
      {canConnectMachines(role) ? (
        <ConnectForm team={me.active_team.display_name} onMinted={load} />
      ) : (
        <div className="mb-6 text-sm text-[var(--muted)]">
          Editors and owners connect machines to this team.
        </div>
      )}
      <Panel
        title="Machine tokens"
        hint="Each token lets one machine claim this team's work. Revoke a token to disconnect its machine."
      >
        {loadError ? (
          <div className="p-4 text-sm text-red-300">{loadError}</div>
        ) : tokens === null ? (
          <div className="p-4 text-xs text-[var(--muted)]">Loading…</div>
        ) : tokens.length === 0 ? (
          <div className="p-4 text-xs text-[var(--muted)]">
            No machines connected yet.
          </div>
        ) : (
          <ul>
            {tokens.map((t) => (
              <li
                key={t.prefix}
                className="flex items-center gap-3 px-4 py-3 border-b border-[var(--border)] last:border-0"
              >
                <div className="flex-1 min-w-0">
                  <div className="text-sm truncate">{t.name || t.prefix}</div>
                  <div className="text-xs text-[var(--muted)] font-mono truncate">
                    {t.prefix}
                    {t.created_by ? ` · by ${t.created_by}` : ""}
                    {t.created_at ? ` · ${fmtDateTime(unixSecondsISO(t.created_at))}` : ""}
                    {t.expires_at
                      ? ` · expires ${fmtDateTime(unixSecondsISO(t.expires_at))}`
                      : ""}
                  </div>
                </div>
                {t.last_used_at ? (
                  <span className="text-xs text-[var(--muted)]">
                    last seen {fmtDateTime(unixSecondsISO(t.last_used_at))}
                  </span>
                ) : null}
                {mayRevoke ? (
                  <button
                    type="button"
                    className={dangerButtonClass}
                    disabled={busy !== null}
                    onClick={() => revoke(t)}
                  >
                    Revoke
                  </button>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </Panel>
    </>
  );
}

function ConnectForm({
  team,
  onMinted,
}: {
  team: string;
  onMinted: () => Promise<void>;
}) {
  const [name, setName] = useState("");
  const [minting, setMinting] = useState(false);
  const [minted, setMinted] = useState<
    (MintedRunnerToken & { name: string }) | null
  >(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    const n = name.trim();
    if (!n) return;
    setMinting(true);
    try {
      const out = await mintRunnerToken(n);
      setMinted({ ...out, name: n });
      setName("");
      await onMinted();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setMinting(false);
    }
  }

  const command = minted ? runnerConnectCommand(minted, minted.name) : "";

  return (
    <Panel
      title="Connect a machine"
      hint={`Run sparkwing-runner on a laptop or server so it picks up ${team}'s work.`}
    >
      <form onSubmit={submit} className="flex flex-wrap items-center gap-2 p-4">
        <input
          required
          maxLength={64}
          placeholder="machine name, e.g. build-box"
          aria-label="Machine name"
          className={`${inputClass} flex-1 min-w-[14rem]`}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <button type="submit" className={buttonClass} disabled={minting}>
          {minting ? "Creating token…" : "Create token"}
        </button>
      </form>
      {minted ? (
        <div className="px-4 pb-4 space-y-3">
          <div className="rounded-[var(--radius-control)] border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
            This token is shown once. Copy the command now; Sparkwing keeps
            only its prefix, <span className="font-mono">{minted.prefix}</span>.
          </div>
          <div>
            <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1">
              Run on {minted.name}
            </div>
            <CopyField value={command} label="Connect command" multiline />
            {minted.command ? null : (
              <div className="text-xs text-[var(--muted)] mt-1">
                Replace{" "}
                <span className="font-mono">{controllerURLPlaceholder}</span>{" "}
                with the URL this machine reaches the controller on.
              </div>
            )}
          </div>
          <div>
            <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1">
              Token
            </div>
            <CopyField value={minted.token} label="Runner token" />
          </div>
        </div>
      ) : null}
    </Panel>
  );
}
