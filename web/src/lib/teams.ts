import { authFetch } from "./api";

export type Role = "owner" | "editor" | "reader";

export interface Capabilities {
  mode: string;
  teams?: { enabled: boolean };
  auth?: { providers: string[] };
}

export interface TeamRef {
  slug: string;
  display_name: string;
  role: Role;
}

export interface PendingInvitation {
  id: string;
  team_slug: string;
  team_display_name: string;
  role: Role;
}

export interface Me {
  user: { id: string; email: string; name: string };
  active_team: TeamRef;
  memberships: TeamRef[];
  invitations: PendingInvitation[];
}

export function teamsEnabled(caps: Capabilities | null): boolean {
  return caps?.teams?.enabled === true;
}

const roleRank: Record<Role, number> = { reader: 0, editor: 1, owner: 2 };

// These decide what the dashboard offers; the controller decides what happens.
export function canManageTeam(role: Role | undefined): boolean {
  return role === "owner";
}

export function canConnectMachines(role: Role | undefined): boolean {
  return role !== undefined && roleRank[role] >= roleRank.editor;
}

export function assignableRoles(role: Role | undefined): Role[] {
  if (role === undefined) return [];
  return (Object.keys(roleRank) as Role[])
    .filter((r) => roleRank[r] <= roleRank[role])
    .sort((a, b) => roleRank[b] - roleRank[a]);
}

export const lastOwnerNote = "A team needs an owner - promote someone first";

// The controller refuses to demote or remove the last owner; the page says so
// before the click rather than after it.
export function isLastOwner(
  members: { user_id: string; role: Role }[],
  userID: string,
): boolean {
  const owners = members.filter((m) => m.role === "owner");
  return owners.length === 1 && owners[0].user_id === userID;
}

// The store keys tenants by slug and uses it in hostnames, so it is DNS-safe.
export function teamSlugProblem(slug: string): string | null {
  if (slug.length < 2 || slug.length > 40) return "Use 2 to 40 characters.";
  if (!/^[a-z0-9-]+$/.test(slug)) {
    return "Use lowercase letters, digits and dashes.";
  }
  if (slug.startsWith("-") || slug.endsWith("-")) {
    return "Start and end with a letter or digit.";
  }
  return null;
}

export function slugFromName(name: string): string {
  return name
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[̀-ͯ]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40)
    .replace(/-+$/g, "");
}

export class TeamApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

async function failure(res: Response, action: string): Promise<TeamApiError> {
  let detail = "";
  try {
    const text = (await res.text()).trim();
    try {
      const body = JSON.parse(text) as { message?: string; error?: string };
      detail = body.message || body.error || text;
    } catch {
      detail = text;
    }
  } catch {
    detail = "";
  }
  if (res.status === 403 && !detail) detail = "your role does not allow this";
  return new TeamApiError(
    res.status,
    detail ? `${action}: ${detail}` : `${action} failed (${res.status})`,
  );
}

async function send(
  method: string,
  url: string,
  action: string,
  body?: unknown,
): Promise<Response> {
  const res = await authFetch(url, {
    method,
    headers:
      body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await failure(res, action);
  return res;
}

export async function getCapabilities(): Promise<Capabilities | null> {
  try {
    const res = await authFetch("/api/v1/capabilities");
    if (!res.ok) return null;
    return (await res.json()) as Capabilities;
  } catch {
    return null;
  }
}

export type MeResult =
  { kind: "member"; me: Me } | { kind: "operator" } | { kind: "unavailable" };

// A password-signed-in operator holds no team identity, and the controller
// refuses /me for that session; the refusal means "no account", not "signed out".
export async function getMe(): Promise<MeResult> {
  try {
    const res = await authFetch("/api/v1/me", {}, { speaksForSession: false });
    if ([401, 403, 404].includes(res.status)) return { kind: "operator" };
    if (!res.ok) return { kind: "unavailable" };
    const body = (await res.json()) as Partial<Me> & { operator?: boolean };
    if (!body.user || !body.active_team || body.operator) {
      return { kind: "operator" };
    }
    return {
      kind: "member",
      me: {
        ...(body as Me),
        memberships: body.memberships ?? [],
        invitations: body.invitations ?? [],
      },
    };
  } catch {
    return { kind: "unavailable" };
  }
}

export async function switchTeam(slug: string): Promise<void> {
  await send("POST", "/api/v1/me/active-team", "Switch team", { slug });
}

export interface Team {
  slug: string;
  display_name: string;
}

export interface Member {
  user_id: string;
  email: string;
  name: string;
  role: Role;
}

export interface TeamInvitation {
  id: string;
  email: string;
  role: Role;
  expires_at?: string;
  accept_url?: string;
}

export interface CreatedInvitation {
  id: string;
  accept_url: string;
  // False when the controller mailed nothing: no mail service, or the
  // address reached its daily limit. The link still works either way.
  email_sent?: boolean;
}

export interface RunnerToken {
  prefix: string;
  name?: string;
  created_by?: string;
  // Unix seconds, as the controller reports them.
  created_at?: number;
  expires_at?: number;
  last_used_at?: number;
}

// unixSecondsISO turns the controller's unix-second stamps into the ISO
// string the time formatters read; Date takes milliseconds.
export function unixSecondsISO(seconds?: number): string {
  if (!seconds) return "";
  return new Date(seconds * 1000).toISOString();
}

export interface MintedRunnerToken {
  token: string;
  prefix: string;
  command: string;
}

// A list route may answer with a bare array or wrap it under one key; both read
// the same so the page does not break on either spelling.
export function asList<T>(body: unknown, key: string): T[] {
  if (Array.isArray(body)) return body as T[];
  if (body && typeof body === "object") {
    const inner = (body as Record<string, unknown>)[key];
    if (Array.isArray(inner)) return inner as T[];
  }
  return [];
}

const seg = encodeURIComponent;

export async function createTeam(
  slug: string,
  displayName: string,
): Promise<Team> {
  const res = await send("POST", "/api/v1/teams", "Create team", {
    slug,
    display_name: displayName,
  });
  return (await res.json()) as Team;
}

export async function renameTeam(displayName: string): Promise<void> {
  await send("PATCH", "/api/v1/team", "Rename team", {
    display_name: displayName,
  });
}

export async function listMembers(): Promise<Member[]> {
  const res = await send("GET", "/api/v1/team/members", "List members");
  return asList<Member>(await res.json(), "members");
}

export async function changeMemberRole(
  userID: string,
  role: Role,
): Promise<void> {
  await send("PATCH", `/api/v1/team/members/${seg(userID)}`, "Change role", {
    role,
  });
}

export async function removeMember(userID: string): Promise<void> {
  await send("DELETE", `/api/v1/team/members/${seg(userID)}`, "Remove member");
}

export async function listInvitations(): Promise<TeamInvitation[]> {
  const res = await send("GET", "/api/v1/team/invitations", "List invitations");
  return asList<TeamInvitation>(await res.json(), "invitations");
}

export async function inviteMember(
  email: string,
  role: Role,
): Promise<CreatedInvitation> {
  const res = await send("POST", "/api/v1/team/invitations", "Invite", {
    email,
    role,
  });
  return (await res.json()) as CreatedInvitation;
}

export async function revokeInvitation(id: string): Promise<void> {
  await send(
    "DELETE",
    `/api/v1/team/invitations/${seg(id)}`,
    "Revoke invitation",
  );
}

export async function acceptInvitation(id: string): Promise<void> {
  await send(
    "POST",
    `/api/v1/invitations/${seg(id)}/accept`,
    "Accept invitation",
  );
}

export async function listRunnerTokens(): Promise<RunnerToken[]> {
  const res = await send("GET", "/api/v1/team/runner-tokens", "List machines");
  return asList<RunnerToken>(await res.json(), "tokens");
}

export async function mintRunnerToken(
  name: string,
  repos: string[],
): Promise<MintedRunnerToken> {
  const res = await send(
    "POST",
    "/api/v1/team/runner-tokens",
    "Connect a machine",
    { name, repos },
  );
  return (await res.json()) as MintedRunnerToken;
}

// The page collects one free-form field; the controller validates each pattern.
export function parseRepoPatterns(input: string): string[] {
  return input
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
}

export async function revokeRunnerToken(prefix: string): Promise<void> {
  await send(
    "DELETE",
    `/api/v1/team/runner-tokens/${seg(prefix)}`,
    "Revoke machine token",
  );
}

// The controller composes the connect command because only it knows the URL a
// machine reaches it on. Without one, the same command carries a placeholder the
// reader fills in, and the page says so.
export const controllerURLPlaceholder = "<controller-url>";

export function runnerConnectCommand(
  minted: MintedRunnerToken,
  name: string,
  repos: string[],
): string {
  if (minted.command) return minted.command;
  const quoted = (s: string) =>
    /^[A-Za-z0-9._:/@=-]+$/.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`;
  return [
    `SPARKWING_AGENT_TOKEN=${quoted(minted.token)}`,
    "sparkwing-runner runner",
    `--controller ${controllerURLPlaceholder}`,
    ...repos.map((r) => `--allow-repo '${r.replace(/'/g, `'\\''`)}'`),
    "--also-claim-triggers --max-claims-before-restart 0 --metrics-addr=",
    `--holder-prefix ${quoted(name)}`,
  ].join(" ");
}

export interface CLIToken {
  prefix: string;
  scopes?: string[];
  // Unix seconds, as the controller reports them.
  created_at?: number;
  expires_at?: number | null;
  last_used_at?: number | null;
}

export interface MintedCLIToken {
  token: string;
  prefix: string;
  scopes: string[];
  expires_at: number;
  profile: string;
  setup: string;
  run: string;
}

export async function listCLITokens(): Promise<CLIToken[]> {
  const res = await send("GET", "/api/v1/team/cli-tokens", "List CLI tokens");
  return asList<CLIToken>(await res.json(), "tokens");
}

export async function mintCLIToken(): Promise<MintedCLIToken> {
  const res = await send("POST", "/api/v1/team/cli-tokens", "Create CLI token");
  return (await res.json()) as MintedCLIToken;
}

export async function revokeCLIToken(prefix: string): Promise<void> {
  await send(
    "DELETE",
    `/api/v1/team/cli-tokens/${seg(prefix)}`,
    "Revoke CLI token",
  );
}

// A CLI token without runs.write reads runs but cannot start one, which is
// what a reader's token is; the page says so rather than letting the first
// trigger find out.
export function cliTokenStartsRuns(scopes: string[] | undefined): boolean {
  return (scopes ?? []).includes("runs.write");
}

export interface TeamDeletion {
  slug: string;
  state: "pending" | "done";
  // Unix seconds, as the controller reports them.
  requested_at: number;
  finished_at: number | null;
  attempts: number;
  last_error: string;
}

// An account holds at least one team, so the last one goes with the account
// rather than on its own; the controller refuses it with 409 either way.
export function canDeleteTeam(me: Me): boolean {
  return canManageTeam(me.active_team.role) && me.memberships.length > 1;
}

// The typed confirmation matches the way the controller compares it: slugs
// and addresses are lowercased and trimmed there too.
export function confirmationMatches(typed: string, expected: string): boolean {
  const norm = (v: string) => v.trim().toLowerCase();
  return norm(typed) !== "" && norm(typed) === norm(expected);
}

export async function deleteTeam(confirmSlug: string): Promise<TeamDeletion> {
  const res = await send("DELETE", "/api/v1/team", "Delete team", {
    confirm_slug: confirmSlug,
  });
  return (await res.json()) as TeamDeletion;
}

export async function listTeamDeletions(): Promise<TeamDeletion[]> {
  const res = await send(
    "GET",
    "/api/v1/me/team-deletions",
    "List team deletions",
  );
  return asList<TeamDeletion>(await res.json(), "deletions");
}

export type AccountDeletionResult =
  | { kind: "deleted"; deletedTeams: string[] }
  | { kind: "blocked"; teams: TeamRef[] }
  | { kind: "reauth" };

// A 409 is an answer, not a failure: it names the teams that would be left
// without an owner, which the page lists so the person can deal with each.
export async function deleteAccount(
  confirmEmail: string,
): Promise<AccountDeletionResult> {
  const res = await authFetch("/api/v1/me", {
    method: "DELETE",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ confirm_email: confirmEmail }),
  });
  if (res.status === 409) {
    const body = (await res.json()) as { teams?: TeamRef[] };
    return { kind: "blocked", teams: body.teams ?? [] };
  }
  // The controller deletes an account only for a sign-in from the last few
  // minutes; the page asks for a fresh one rather than showing an error.
  if (res.status === 403) {
    const body = (await res
      .clone()
      .json()
      .catch(() => ({}))) as {
      error?: string;
    };
    if (body.error === "reauth_required") return { kind: "reauth" };
  }
  if (!res.ok) throw await failure(res, "Delete account");
  const body = (await res.json()) as { deleted_teams?: string[] };
  return { kind: "deleted", deletedTeams: body.deleted_teams ?? [] };
}
