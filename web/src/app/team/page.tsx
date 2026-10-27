"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
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
import { RolePill } from "@/components/TeamSwitcher";
import { toast } from "@/components/Toasts";
import {
  type CreatedInvitation,
  type Me,
  type Member,
  type Role,
  type TeamInvitation,
  type TeamSettings,
  assignableRoles,
  canDeleteTeam,
  canManageTeam,
  changeMemberRole,
  confirmationMatches,
  deleteTeam,
  inviteMember,
  isLastOwner,
  lastOwnerNote,
  listInvitations,
  listMembers,
  getTeamSettings,
  setTeamSettings,
  removeMember,
  revokeInvitation,
} from "@/lib/teams";
import { fmtDateTime } from "@/lib/timeFormat";
import { refreshTeamState } from "@/lib/useTeam";

export default function TeamMembersPage() {
  return <TeamShell>{(me) => <Members me={me} />}</TeamShell>;
}

function Members({ me }: { me: Me }) {
  const role = me.active_team.role;
  const owner = canManageTeam(role);
  const router = useRouter();
  const [members, setMembers] = useState<Member[] | null>(null);
  const [invitations, setInvitations] = useState<TeamInvitation[] | null>(null);
  const [loadError, setLoadError] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [settings, setSettings] = useState<TeamSettings | null>(null);

  const load = useCallback(async () => {
    try {
      setMembers(await listMembers());
      setSettings(await getTeamSettings());
      setLoadError("");
    } catch (err) {
      setLoadError(errorText(err));
    }
    if (owner) {
      try {
        setInvitations(await listInvitations());
      } catch (err) {
        toast(errorText(err), "error");
      }
    }
  }, [owner]);

  useEffect(() => {
    load();
  }, [load]);

  async function run(key: string, action: () => Promise<void>, done: string) {
    setBusy(key);
    try {
      await action();
      toast(done, "success");
      await load();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setBusy(null);
    }
  }

  async function leave(self: Member) {
    if (!window.confirm(`Leave ${me.active_team.display_name}?`)) return;
    setBusy(self.user_id);
    try {
      await removeMember(self.user_id);
      await refreshTeamState();
      router.push("/");
    } catch (err) {
      setBusy(null);
      toast(errorText(err), "error");
    }
  }

  const choices = assignableRoles(role);

  return (
    <>
      <Panel title="Team settings" hint="Shared runners always require team administration. Turning off personal enrollment ends the personal machines members enrolled; turning it back on does not revive them.">
        <label className="flex items-center gap-3 p-4 text-sm">
          <input
            type="checkbox"
            checked={settings?.members_can_add_machines ?? false}
            disabled={!owner || settings === null || busy !== null}
            onChange={(e) => run("settings", async () => {
              setSettings(await setTeamSettings({ members_can_add_machines: e.target.checked }));
            }, "Team settings saved")}
          />
          Members can add personal machines (members_can_add_machines)
        </label>
        <label className="flex items-center gap-3 p-4 text-sm" title="Off by default. On, verified runs on plain-machine, persistent and self-hosted Actions runners write the verified cache that fresh pods, Sparkwing Cloud and remote readers restore. Off, they write only their own host's cache, because an earlier job on that host could have tampered with it. Changes are audited.">
          <input
            type="checkbox"
            checked={settings?.high_trust ?? false}
            disabled={!owner || settings === null || busy !== null}
            onChange={(e) => run("settings", async () => {
              setSettings(await setTeamSettings({ high_trust: e.target.checked }));
            }, "Team settings saved")}
          />
          High trust: persistent runners write the shared verified cache (high_trust)
        </label>
        {settings && (
          <form className="flex flex-wrap items-center gap-3 p-4 text-sm" onSubmit={(e) => {
            e.preventDefault();
            const ceiling = Number(new FormData(e.currentTarget).get("max_children_per_root"));
            if (Number.isSafeInteger(ceiling) && ceiling > 0 && ceiling <= 1000) {
              run("settings", async () => {
                setSettings(await setTeamSettings({ max_children_per_root: ceiling }));
              }, "Team settings saved");
            }
          }}>
            <label htmlFor="max-children-per-root">Maximum child runs per root</label>
            <input
              key={settings.max_children_per_root}
              id="max-children-per-root"
              name="max_children_per_root"
              type="number"
              min="1"
              max="1000"
              step="1"
              required
              title="1 to 1000 descendants across every depth, excluding retries. Default 50. Changes are audited."
              defaultValue={settings.max_children_per_root}
              disabled={!owner || busy !== null}
              className={`${inputClass} w-24`}
            />
            <button type="submit" className={buttonClass} disabled={!owner || busy !== null}>Save</button>
          </form>
        )}
      </Panel>
      <Panel
        title="Members"
        hint={
          owner
            ? "Owners manage members, invitations and shared runners. Members connect personal machines when team settings allow it."
            : "Only owners change roles or invite people."
        }
      >
        {loadError ? (
          <div className="p-4 text-sm text-red-300">{loadError}</div>
        ) : members === null ? (
          <div className="p-4 text-xs text-[var(--muted)]">Loading…</div>
        ) : members.length === 0 ? (
          <div className="p-4 text-xs text-[var(--muted)]">No members.</div>
        ) : (
          <ul>
            {members.map((m) => {
              const self = m.user_id === me.user.id;
              const lastOwner = isLastOwner(members, m.user_id);
              return (
                <li
                  key={m.user_id}
                  className="flex items-center gap-3 px-4 py-3 border-b border-[var(--border)] last:border-0"
                >
                  <Avatar name={m.name || m.email} />
                  <div className="flex-1 min-w-0">
                    <div className="text-sm truncate">
                      {m.name || m.email}
                      {self ? (
                        <span className="ml-2 text-xs text-[var(--muted)]">
                          (you)
                        </span>
                      ) : null}
                    </div>
                    {m.name ? (
                      <div className="text-xs text-[var(--muted)] truncate">
                        {m.email}
                      </div>
                    ) : null}
                  </div>
                  {owner && !self ? (
                    <select
                      aria-label={`Role for ${m.email}`}
                      className={`${inputClass} py-1 text-xs`}
                      value={m.role}
                      disabled={busy !== null || lastOwner}
                      title={lastOwner ? lastOwnerNote : undefined}
                      onChange={(e) =>
                        run(
                          m.user_id,
                          () =>
                            changeMemberRole(m.user_id, e.target.value as Role),
                          `${m.email} is now ${e.target.value}`,
                        )
                      }
                    >
                      {choices.map((r) => (
                        <option key={r} value={r}>
                          {r}
                        </option>
                      ))}
                    </select>
                  ) : (
                    <RolePill role={m.role} />
                  )}
                  {self ? (
                    <button
                      type="button"
                      className={quietButtonClass}
                      disabled={busy !== null || lastOwner}
                      title={lastOwner ? lastOwnerNote : undefined}
                      onClick={() => leave(m)}
                    >
                      Leave
                    </button>
                  ) : owner ? (
                    <button
                      type="button"
                      className={dangerButtonClass}
                      disabled={busy !== null || lastOwner}
                      title={lastOwner ? lastOwnerNote : undefined}
                      onClick={() => {
                        if (
                          window.confirm(
                            `Remove ${m.email} from ${me.active_team.display_name}?`,
                          )
                        ) {
                          run(
                            m.user_id,
                            () => removeMember(m.user_id),
                            `Removed ${m.email}`,
                          );
                        }
                      }}
                    >
                      Remove
                    </button>
                  ) : null}
                </li>
              );
            })}
          </ul>
        )}
      </Panel>

      {owner ? (
        <>
          <InviteForm roles={choices} onInvited={load} />
          <Panel title="Pending invitations">
            {invitations === null ? (
              <div className="p-4 text-xs text-[var(--muted)]">Loading…</div>
            ) : invitations.length === 0 ? (
              <div className="p-4 text-xs text-[var(--muted)]">
                No pending invitations.
              </div>
            ) : (
              <ul>
                {invitations.map((inv) => (
                  <li
                    key={inv.id}
                    className="flex items-center gap-3 px-4 py-3 border-b border-[var(--border)] last:border-0"
                  >
                    <div className="flex-1 min-w-0">
                      <div className="text-sm truncate">{inv.email}</div>
                      {inv.expires_at ? (
                        <div className="text-xs text-[var(--muted)]">
                          expires {fmtDateTime(inv.expires_at)}
                        </div>
                      ) : null}
                    </div>
                    <RolePill role={inv.role} />
                    <button
                      type="button"
                      className={dangerButtonClass}
                      disabled={busy !== null}
                      onClick={() =>
                        run(
                          inv.id,
                          () => revokeInvitation(inv.id),
                          `Revoked the invitation for ${inv.email}`,
                        )
                      }
                    >
                      Revoke
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </Panel>
          <DeleteTeamPanel me={me} />
        </>
      ) : null}
    </>
  );
}

function DeleteTeamPanel({ me }: { me: Me }) {
  const team = me.active_team;
  const router = useRouter();
  const [typed, setTyped] = useState("");
  const [deleting, setDeleting] = useState(false);
  const allowed = canDeleteTeam(me);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!confirmationMatches(typed, team.slug)) return;
    setDeleting(true);
    try {
      await deleteTeam(team.slug);
      toast(`Deleting ${team.display_name}`, "success");
      await refreshTeamState();
      router.push("/account");
    } catch (err) {
      setDeleting(false);
      toast(errorText(err), "error");
    }
  }

  return (
    <Panel title="Delete team">
      <div className="p-4 space-y-3 text-sm">
        <p className="text-[var(--muted)]">
          Deleting {team.display_name} removes every member from it at once,
          revokes its runner and CLI tokens, and cancels its queued and running
          runs. Within a few minutes the controller then deletes its runs, logs,
          secrets, schedules, cached artifacts and settings, and clears its
          cache once more a few hours later. This cannot be undone, and the slug{" "}
          <span className="font-mono">{team.slug}</span> can never be used for a
          team again.
        </p>
        {allowed ? (
          <form onSubmit={submit} className="flex flex-wrap items-center gap-2">
            <input
              aria-label="Type the team slug to confirm"
              placeholder={team.slug}
              className={`${inputClass} font-mono flex-1 min-w-[14rem]`}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
            />
            <button
              type="submit"
              className={dangerButtonClass}
              disabled={deleting || !confirmationMatches(typed, team.slug)}
            >
              {deleting ? "Deleting…" : "Delete this team"}
            </button>
          </form>
        ) : (
          <p className="text-xs text-[var(--muted)]">
            This is your only team, so it can be deleted only with your account.
            Deleting your account is on your{" "}
            <Link href="/account" className="underline">
              account page
            </Link>
            .
          </p>
        )}
        {allowed ? (
          <p className="text-xs text-[var(--muted)]">
            Type <span className="font-mono">{team.slug}</span> to confirm.
          </p>
        ) : null}
      </div>
    </Panel>
  );
}

function Avatar({ name }: { name: string }) {
  const letter = (name.trim()[0] || "?").toUpperCase();
  return (
    <div
      aria-hidden="true"
      className="w-8 h-8 rounded-full bg-[var(--surface-raised)] border border-[var(--border)] flex items-center justify-center text-xs font-medium text-[var(--muted)] shrink-0"
    >
      {letter}
    </div>
  );
}

function InviteForm({
  roles,
  onInvited,
}: {
  roles: Role[];
  onInvited: () => Promise<void>;
}) {
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Role>("editor");
  const [sending, setSending] = useState(false);
  const [created, setCreated] = useState<
    (CreatedInvitation & { email: string }) | null
  >(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    const to = email.trim();
    if (!to) return;
    setSending(true);
    try {
      const inv = await inviteMember(to, role);
      setCreated({ ...inv, email: to });
      setEmail("");
      await onInvited();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setSending(false);
    }
  }

  return (
    <Panel title="Invite someone">
      <form onSubmit={submit} className="flex flex-wrap items-center gap-2 p-4">
        <input
          type="email"
          required
          placeholder="name@company.com"
          aria-label="Email"
          className={`${inputClass} flex-1 min-w-[14rem]`}
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
        <select
          aria-label="Role"
          className={inputClass}
          value={role}
          onChange={(e) => setRole(e.target.value as Role)}
        >
          {roles.map((r) => (
            <option key={r} value={r}>
              {r}
            </option>
          ))}
        </select>
        <button type="submit" className={buttonClass} disabled={sending}>
          {sending ? "Inviting…" : "Invite"}
        </button>
      </form>
      {created ? (
        <div className="px-4 pb-4 space-y-2">
          <div className="text-xs text-[var(--muted)]">
            {created.email_sent
              ? "Emailed an invitation to "
              : "No email was sent. Send this link to "}
            <span className="text-[var(--foreground)]">{created.email}</span>
            {created.email_sent ? ". You can also send them this link" : ""}; it
            works only when they sign in with that address and expires in 7
            days.
          </div>
          <CopyField value={created.accept_url} label="Invitation link" />
        </div>
      ) : null}
    </Panel>
  );
}
