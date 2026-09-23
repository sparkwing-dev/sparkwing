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
  type CLIToken,
  type Me,
  type MintedCLIToken,
  type MintedRunnerToken,
  type RunnerToken,
  canConnectMachines,
  canManageTeam,
  cliTokenStartsRuns,
  controllerURLPlaceholder,
  listCLITokens,
  listRunnerTokens,
  mintCLIToken,
  mintRunnerToken,
  parseRepoPatterns,
  revokeCLIToken,
  revokeRunnerToken,
  runnerConnectCommand,
  unixSecondsISO,
} from "@/lib/teams";
import { setMachineGitCredentials } from "@/lib/gitCredentials";
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

  async function setGitCredentials(t: RunnerToken, enabled: boolean) {
    const label = t.name || t.prefix;
    setBusy(t.prefix);
    try {
      await setMachineGitCredentials(t.prefix, enabled);
      toast(
        enabled
          ? `${label} now receives git credentials`
          : `${label} no longer receives git credentials`,
        "success",
      );
      await load();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setBusy(null);
    }
  }

  const owner = canManageTeam(role);

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
        hint="Each token lets one machine claim this team's work. Revoke a token to disconnect its machine. A machine gets the team's git credentials only when an owner turns them on for it."
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
                    {t.created_at
                      ? ` · ${fmtDateTime(unixSecondsISO(t.created_at))}`
                      : ""}
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
                {owner ? (
                  <label className="flex items-center gap-1.5 text-xs text-[var(--muted)]">
                    <input
                      type="checkbox"
                      checked={t.git_credentials === true}
                      disabled={busy !== null}
                      onChange={(e) => setGitCredentials(t, e.target.checked)}
                    />
                    Receives git credentials
                  </label>
                ) : t.git_credentials ? (
                  <span className="text-xs text-[var(--muted)]">
                    receives git credentials
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
      <CLIAccess team={me.active_team.display_name} />
    </>
  );
}

function CLIAccess({ team }: { team: string }) {
  const [tokens, setTokens] = useState<CLIToken[] | null>(null);
  const [loadError, setLoadError] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [minted, setMinted] = useState<MintedCLIToken | null>(null);

  const load = useCallback(async () => {
    try {
      setTokens(await listCLITokens());
      setLoadError("");
    } catch (err) {
      setLoadError(errorText(err));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function mint() {
    setBusy("mint");
    try {
      setMinted(await mintCLIToken());
      await load();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setBusy(null);
    }
  }

  async function revoke(t: CLIToken) {
    if (
      !window.confirm(
        `Revoke ${t.prefix}? A terminal using this token is refused at once.`,
      )
    ) {
      return;
    }
    setBusy(t.prefix);
    try {
      await revokeCLIToken(t.prefix);
      if (minted?.prefix === t.prefix) setMinted(null);
      toast(`Revoked ${t.prefix}`, "success");
      await load();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="mt-6">
      <Panel
        title="CLI access"
        hint={`A personal token lets the sparkwing CLI start and follow ${team}'s runs as you. It lapses after 90 days and goes when you leave the team.`}
      >
        <div className="flex items-center gap-2 p-4">
          <button
            type="button"
            className={buttonClass}
            disabled={busy !== null}
            onClick={mint}
          >
            {busy === "mint" ? "Creating token…" : "Create CLI token"}
          </button>
        </div>
        {minted ? (
          <div className="px-4 pb-4 space-y-3">
            <div className="rounded-[var(--radius-control)] border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
              This token is shown once. Sparkwing keeps only its prefix,{" "}
              <span className="font-mono">{minted.prefix}</span>.
            </div>
            {cliTokenStartsRuns(minted.scopes) ? null : (
              <div className="text-xs text-[var(--muted)]">
                Your role reads runs, so this token cannot start one.
              </div>
            )}
            <div>
              <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1">
                1. Save it as the {minted.profile} profile
              </div>
              <CopyField value={minted.setup} label="Setup command" multiline />
              <div className="text-xs text-[var(--muted)] mt-1">
                Paste the token below when the command asks for it.
              </div>
            </div>
            <div>
              <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1">
                Token
              </div>
              <CopyField value={minted.token} label="CLI token" />
            </div>
            {cliTokenStartsRuns(minted.scopes) ? (
              <div>
                <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1">
                  2. Start a run from a checkout of a pushed commit
                </div>
                <CopyField value={minted.run} label="Run command" />
              </div>
            ) : null}
          </div>
        ) : null}
        {loadError ? (
          <div className="p-4 text-sm text-red-300">{loadError}</div>
        ) : tokens === null ? (
          <div className="p-4 text-xs text-[var(--muted)]">Loading…</div>
        ) : tokens.length === 0 ? (
          <div className="px-4 pb-4 text-xs text-[var(--muted)]">
            You hold no CLI tokens in this team.
          </div>
        ) : (
          <ul className="border-t border-[var(--border)]">
            {tokens.map((t) => (
              <li
                key={t.prefix}
                className="flex items-center gap-3 px-4 py-3 border-b border-[var(--border)] last:border-0"
              >
                <div className="flex-1 min-w-0">
                  <div className="text-sm font-mono truncate">{t.prefix}</div>
                  <div className="text-xs text-[var(--muted)] truncate">
                    {cliTokenStartsRuns(t.scopes) ? "starts runs" : "read-only"}
                    {t.created_at
                      ? ` · ${fmtDateTime(unixSecondsISO(t.created_at))}`
                      : ""}
                    {t.expires_at
                      ? ` · expires ${fmtDateTime(unixSecondsISO(t.expires_at))}`
                      : ""}
                    {t.last_used_at
                      ? ` · last used ${fmtDateTime(unixSecondsISO(t.last_used_at))}`
                      : ""}
                  </div>
                </div>
                <button
                  type="button"
                  className={dangerButtonClass}
                  disabled={busy !== null}
                  onClick={() => revoke(t)}
                >
                  Revoke
                </button>
              </li>
            ))}
          </ul>
        )}
      </Panel>
    </div>
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
  const [repos, setRepos] = useState("");
  const [minting, setMinting] = useState(false);
  const [minted, setMinted] = useState<
    (MintedRunnerToken & { name: string; repos: string[] }) | null
  >(null);

  const repoPatterns = parseRepoPatterns(repos);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    const n = name.trim();
    if (!n || repoPatterns.length === 0) return;
    setMinting(true);
    try {
      const out = await mintRunnerToken(n, repoPatterns);
      setMinted({ ...out, name: n, repos: repoPatterns });
      setName("");
      setRepos("");
      await onMinted();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setMinting(false);
    }
  }

  const command = minted
    ? runnerConnectCommand(minted, minted.name, minted.repos)
    : "";

  return (
    <Panel
      title="Connect a machine"
      hint={`Run sparkwing-runner on a laptop or server so it picks up ${team}'s work.`}
    >
      <form onSubmit={submit} className="flex flex-col gap-2 p-4">
        <div className="flex flex-wrap items-center gap-2">
          <input
            required
            maxLength={64}
            placeholder="machine name, e.g. build-box"
            aria-label="Machine name"
            className={`${inputClass} flex-1 min-w-[14rem]`}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <button
            type="submit"
            className={buttonClass}
            disabled={minting || repoPatterns.length === 0}
          >
            {minting ? "Creating token…" : "Create token"}
          </button>
        </div>
        <div>
          <label
            htmlFor="machine-repos"
            className="block text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1"
          >
            Repositories this machine may build
          </label>
          <textarea
            id="machine-repos"
            required
            rows={2}
            placeholder="github.com/acme/*"
            className={`${inputClass} w-full font-mono`}
            value={repos}
            onChange={(e) => setRepos(e.target.value)}
          />
          <p className="text-xs text-[var(--muted)] mt-1">
            This machine will run pipeline code from these repositories as your
            user.
          </p>
        </div>
      </form>
      {minted ? (
        <div className="px-4 pb-4 space-y-3">
          <div className="rounded-[var(--radius-control)] border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
            This token is shown once. Copy the command now; Sparkwing keeps only
            its prefix, <span className="font-mono">{minted.prefix}</span>.
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
