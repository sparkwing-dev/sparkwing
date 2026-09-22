"use client";

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
  assignableRoles,
  canManageTeam,
  changeMemberRole,
  inviteMember,
  isLastOwner,
  lastOwnerNote,
  listInvitations,
  listMembers,
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

  const load = useCallback(async () => {
    try {
      setMembers(await listMembers());
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
      <Panel
        title="Members"
        hint={
          owner
            ? "Owners manage members, invitations and machine tokens. Editors run pipelines and connect machines. Readers see runs and logs."
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
        </>
      ) : null}
    </>
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
            Invitation email is not sent yet. Send this link to{" "}
            <span className="text-[var(--foreground)]">{created.email}</span>;
            it works only when they sign in with that address.
          </div>
          <CopyField value={created.accept_url} label="Invitation link" />
        </div>
      ) : null}
    </Panel>
  );
}
