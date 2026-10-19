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
  type Member,
  type MintedMachineToken,
  type MintedMemberToken,
  type Role,
  type TeamToken,
  canConnectMachines,
  canManageTeam,
  cliTokenStartsRuns,
  listMembers,
  getTeamSettings,
  listTeamTokens,
  memberLabel,
  memberScopesForRole,
  mintMachineToken,
  mintMemberToken,
  parseRepoPatterns,
  revokeTeamToken,
  runnerConnectCommand,
} from "@/lib/teams";
import {
  approveMachineAutomation,
  requestMachineAutomation,
  setMachineGitCredentials,
} from "@/lib/gitCredentials";
import { fmtDateTime } from "@/lib/timeFormat";

export default function MachinesPage() {
  return <TeamShell>{(me) => <Machines me={me} />}</TeamShell>;
}

function Machines({ me }: { me: Me }) {
  const role = me.active_team.role;
  const [tokens, setTokens] = useState<TeamToken[] | null>(null);
  const [members, setMembers] = useState<Member[]>([]);
  const [loadError, setLoadError] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [membersCanAddMachines, setMembersCanAddMachines] = useState(false);

  const load = useCallback(async () => {
    try {
      setTokens(await listTeamTokens());
      setMembersCanAddMachines((await getTeamSettings()).members_can_add_machines);
      setLoadError("");
    } catch (err) {
      setLoadError(errorText(err));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  // Members only name each token's creator, so the token list stands without them.
  useEffect(() => {
    listMembers()
      .then(setMembers)
      .catch(() => {});
  }, []);

  async function revoke(t: TeamToken) {
    const label = t.subject.id || t.prefix;
    if (
      !window.confirm(
        `Revoke ${label}? A machine using this token stops taking work at once.`,
      )
    ) {
      return;
    }
    setBusy(t.prefix);
    try {
      await revokeTeamToken(t.prefix);
      toast(`Revoked ${label}`, "success");
      await load();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setBusy(null);
    }
  }

  async function setGitCredentials(t: TeamToken, enabled: boolean) {
    const label = t.subject.id || t.prefix;
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

  async function setAutomation(t: TeamToken, change: "request" | "withdraw" | "approve" | "revoke") {
    const label = t.subject.id || t.prefix;
    setBusy(t.prefix);
    try {
      const state =
        change === "request" || change === "withdraw"
          ? await requestMachineAutomation(t.subject.id, change === "request")
          : await approveMachineAutomation(t.subject.id, change === "approve");
      toast(`${label} automated runs: ${state}`, "success");
      await load();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setBusy(null);
    }
  }

  const owner = canManageTeam(role);
  const machines = tokens?.filter((t) => t.subject.kind === "machine") ?? null;
  const cliTokens = tokens?.filter((t) => t.subject.kind === "account") ?? null;

  const mayRevoke = role !== undefined;

  return (
    <>
      {canConnectMachines(role, membersCanAddMachines) ? (
        <ConnectForm team={me.active_team.display_name} admin={owner} onMinted={load} />
      ) : (
        <div className="mb-6 text-sm text-[var(--muted)]">
          Personal enrollment requires an administrator while members_can_add_machines is off.
        </div>
      )}
      <Panel
        title="Machine tokens"
        hint="Personal machines accept only their owner's work, and take the owner's automated runs only after the owner requests it and an administrator approves. Shared runners accept team work and require an administrator to revoke. A machine gets team git credentials only when an owner turns them on for it."
      >
        {loadError ? (
          <div className="p-4 text-sm text-red-300">{loadError}</div>
        ) : machines === null ? (
          <div className="p-4 text-xs text-[var(--muted)]">Loading…</div>
        ) : machines.length === 0 ? (
          <div className="p-4 text-xs text-[var(--muted)]">
            No machines connected yet.
          </div>
        ) : (
          <ul>
            {machines.map((t) => (
              <li
                key={t.prefix}
                className="flex items-center gap-3 px-4 py-3 border-b border-[var(--border)] last:border-0"
              >
                <div className="flex-1 min-w-0">
                  <div className="text-sm truncate">{t.subject.id || t.prefix}</div>
                  <div className="text-xs text-[var(--muted)] font-mono truncate">
                    {t.prefix}
                    {creatorSuffix(t, members)}
                    {t.issued_at
                      ? ` · ${fmtDateTime(t.issued_at)}`
                      : ""}
                    {t.expires_at
                      ? ` · expires ${fmtDateTime(t.expires_at)}`
                      : ""}
                  </div>
                </div>
                {t.last_used_at ? (
                  <span className="text-xs text-[var(--muted)]">
                    last seen {fmtDateTime(t.last_used_at)}
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
                {t.automation ? (
                  <AutomationControl
                    token={t}
                    mine={t.created_by === me.user.id}
                    admin={owner}
                    busy={busy !== null}
                    change={(c) => setAutomation(t, c)}
                  />
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
      <CLIAccess
        team={me.active_team.display_name}
        role={role}
        tokens={cliTokens}
        loadError={loadError}
        reload={load}
      />
    </>
  );
}

function creatorSuffix(t: TeamToken, members: Member[]): string {
  const creator = memberLabel(t.created_by, members);
  return creator ? ` · by ${creator}` : "";
}

function CLIAccess({
  team,
  role,
  tokens,
  loadError,
  reload,
}: {
  team: string;
  role: Role;
  tokens: TeamToken[] | null;
  loadError: string;
  reload: () => Promise<void>;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const [minted, setMinted] = useState<MintedMemberToken | null>(null);

  async function mint() {
    setBusy("mint");
    try {
      setMinted(await mintMemberToken(memberScopesForRole(role)));
      await reload();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setBusy(null);
    }
  }

  async function revoke(t: TeamToken) {
    if (
      !window.confirm(
        `Revoke ${t.prefix}? A terminal using this token is refused at once.`,
      )
    ) {
      return;
    }
    setBusy(t.prefix);
    try {
      await revokeTeamToken(t.prefix);
      if (minted?.prefix === t.prefix) setMinted(null);
      toast(`Revoked ${t.prefix}`, "success");
      await reload();
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
        hint={`A personal token lets the sparkwing CLI start and follow ${team}'s runs as you. It loses access when you leave the team.`}
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
            {cliTokenStartsRuns(minted.grants) ? null : (
              <div className="text-xs text-[var(--muted)]">
                Your role reads runs, so this token cannot start one.
              </div>
            )}
            <div>
              <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1">
                1. Save it as the {minted.team} profile
              </div>
              <CopyField value={`sparkwing cloud connect --controller ${window.location.origin} --name ${minted.team} --token-stdin`} label="Setup command" multiline />
              <div className="text-xs text-[var(--muted)] mt-1">
                Paste the token below when the command asks for it.
              </div>
            </div>
            <div>
              <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1">
                Token
              </div>
              <CopyField value={minted.capability} label="CLI token" />
            </div>
            {cliTokenStartsRuns(minted.grants) ? (
              <div>
                <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1">
                  2. Start a run from a checkout of a pushed commit
                </div>
                <CopyField value={`sparkwing pipeline trigger <pipeline> --profile ${minted.team}`} label="Run command" />
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
                    {cliTokenStartsRuns(t.grants) ? "starts runs" : "read-only"}
                    {t.issued_at
                      ? ` · ${fmtDateTime(t.issued_at)}`
                      : ""}
                    {t.expires_at
                      ? ` · expires ${fmtDateTime(t.expires_at)}`
                      : ""}
                    {t.last_used_at
                      ? ` · last used ${fmtDateTime(t.last_used_at)}`
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
  admin,
  onMinted,
}: {
  team: string;
  admin: boolean;
  onMinted: () => Promise<void>;
}) {
  const [name, setName] = useState("");
  const [repos, setRepos] = useState("");
  const [minting, setMinting] = useState(false);
  const [personal, setPersonal] = useState(true);
  const [minted, setMinted] = useState<
    (MintedMachineToken & { name: string; repos: string[] }) | null
  >(null);

  const repoPatterns = parseRepoPatterns(repos);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    const n = name.trim();
    if (!n || repoPatterns.length === 0) return;
    setMinting(true);
    try {
      const out = await mintMachineToken(n, repoPatterns, personal);
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
    ? runnerConnectCommand(minted, minted.name, minted.repos, window.location.origin)
    : "";

  return (
    <Panel
      title="Connect a machine"
      hint={personal ? "This personal machine accepts only your work." : `This shared runner accepts ${team}'s work.`}
    >
      <form onSubmit={submit} className="flex flex-col gap-2 p-4">
        {admin ? (
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={personal} onChange={(e) => setPersonal(e.target.checked)} />
            Personal machine (only my work)
          </label>
        ) : null}
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
          </div>
          <div>
            <div className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1">
              Token
            </div>
            <CopyField value={minted.capability} label="Runner token" />
          </div>
        </div>
      ) : null}
    </Panel>
  );
}

// AutomationControl lets the owner request automated runs and an administrator approve or revoke them.
function AutomationControl({
  token,
  mine,
  admin,
  busy,
  change,
}: {
  token: TeamToken;
  mine: boolean;
  admin: boolean;
  busy: boolean;
  change: (c: "request" | "withdraw" | "approve" | "revoke") => void;
}) {
  const state = token.automation ?? "none";
  return (
    <span className="flex items-center gap-1.5 text-xs text-[var(--muted)]">
      automated runs: {state}
      {mine && state === "none" ? (
        <button type="button" className={buttonClass} disabled={busy} onClick={() => change("request")}>
          Request
        </button>
      ) : null}
      {mine && state === "requested" ? (
        <button type="button" className={buttonClass} disabled={busy} onClick={() => change("withdraw")}>
          Withdraw
        </button>
      ) : null}
      {admin && state === "requested" ? (
        <button type="button" className={buttonClass} disabled={busy} onClick={() => change("approve")}>
          Approve
        </button>
      ) : null}
      {admin && state === "approved" ? (
        <button type="button" className={dangerButtonClass} disabled={busy} onClick={() => change("revoke")}>
          Revoke approval
        </button>
      ) : null}
    </span>
  );
}
