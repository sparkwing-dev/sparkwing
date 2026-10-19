import { authFetch } from "./api";

export type Role = "owner" | "editor" | "reader";

export interface Capabilities {
  mode: string;
  teams?: { enabled: boolean };
  billing?: { enabled: boolean };
  auth?: { providers: string[] };
  github_app?: { slug: string };
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
  // True while the account waits for admission: it holds no personal space
  // and cannot create a team, but it can join a team that invites it.
  waitlisted?: boolean;
}

// A waitlisted account with no team yet; it has no active team to show.
export type WaitlistedMe = Omit<Me, "active_team">;

export function teamsEnabled(caps: Capabilities | null): boolean {
  return caps?.teams?.enabled === true;
}

export function billingEnabled(caps: Capabilities | null): boolean {
  return caps?.billing?.enabled === true;
}

const roleRank: Record<Role, number> = { reader: 0, editor: 1, owner: 2 };

// These decide what the dashboard offers; the controller decides what happens.
export function canManageTeam(role: Role | undefined): boolean {
  return role === "owner";
}

export function canConnectMachines(role: Role | undefined, membersCanAddMachines = false): boolean {
  return canManageTeam(role) || role !== undefined && membersCanAddMachines;
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

export async function failure(
  res: Response,
  action: string,
): Promise<TeamApiError> {
  let detail = "";
  try {
    const text = (await res.text()).trim();
    try {
      const body = JSON.parse(text) as { error?: { message?: string } };
      detail = body.error?.message || text;
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

export async function send(
  method: string,
  url: string,
  action: string,
  body?: unknown,
): Promise<Response> {
  const res = await authFetch(url, {
    method,
    headers: {
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
      ...(method === "POST" && url === "/api/v1/team/invitations"
        ? { "Idempotency-Key": crypto.randomUUID() }
        : {}),
    },
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
  | { kind: "member"; me: Me }
  | { kind: "waitlisted"; me: WaitlistedMe }
  | { kind: "operator" }
  | { kind: "unavailable" };

// An operator signed in with a token holds no team identity, and the controller
// refuses /me for that session; the refusal means "no account", not "signed out".
export async function getMe(): Promise<MeResult> {
  try {
    const res = await authFetch("/api/v1/me", {}, { speaksForSession: false });
    if ([401, 403, 404].includes(res.status)) return { kind: "operator" };
    if (!res.ok) return { kind: "unavailable" };
    const body = (await res.json()) as Partial<Me> & { operator?: boolean };
    if (body.user && body.waitlisted && !body.active_team && !body.operator) {
      return {
        kind: "waitlisted",
        me: {
          user: body.user,
          memberships: body.memberships ?? [],
          invitations: body.invitations ?? [],
          waitlisted: true,
        },
      };
    }
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

export type TokenFamily = "member" | "machine";

export interface TeamToken {
  prefix: string;
  team: string;
  subject: { kind: "account" | "machine"; id: string };
  audience: "controller";
  grants: string[];
  created_by?: string;
  issued_at: string;
  expires_at?: string;
  last_used_at?: string;
  metered?: boolean;
}

// unixSecondsISO turns the controller's unix-second stamps into the ISO
// string the time formatters read; Date takes milliseconds.
export function unixSecondsISO(seconds?: number): string {
  if (!seconds) return "";
  return new Date(seconds * 1000).toISOString();
}

export type MintedMachineToken = MintedMemberToken;

export interface MintedMemberToken {
  capability: string;
  prefix: string;
  team: string;
  grants: string[];
  expires_at?: string;
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

// memberLabel names an account by the member it belongs to. An account that
// is no longer a member has no name to show, and its raw ID tells a reader
// nothing, so it yields null.
export function memberLabel(
  accountID: string | undefined,
  members: Member[],
): string | null {
  if (!accountID) return null;
  const member = members.find((m) => m.user_id === accountID);
  return member?.name || member?.email || null;
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

export async function listTeamTokens(): Promise<TeamToken[]> {
  const res = await send("GET", "/api/v1/tokens", "List tokens");
  return asList<TeamToken>(await res.json(), "tokens");
}

export async function mintMachineToken(
  name: string,
  repos: string[],
  personal = false,
): Promise<MintedMachineToken> {
  const res = await send("POST", "/api/v1/runners", "Connect a machine", {
    name,
    repos,
    ...(personal ? { personal: true } : {}),
  });
  return (await res.json()) as MintedMachineToken;
}

export type TeamSettings = { members_can_add_machines: boolean };

export async function getTeamSettings(): Promise<TeamSettings> {
  const res = await send("GET", "/api/v1/team/settings", "Read team settings");
  return (await res.json()) as TeamSettings;
}

export async function setTeamSettings(settings: TeamSettings): Promise<TeamSettings> {
  const res = await send("PUT", "/api/v1/team/settings", "Change team settings", settings);
  return (await res.json()) as TeamSettings;
}

const readerScopes = ["runs.read", "logs.read"];
const writerScopes = ["runs.write", "runs.control", "approvals.write"];

// The controller refuses a member scope the role lacks, so the default is the
// role's full set.
export function memberScopesForRole(role: Role): string[] {
  return role === "reader"
    ? [...readerScopes]
    : [...readerScopes, ...writerScopes];
}

export async function mintMemberToken(
  scopes: string[],
): Promise<MintedMemberToken> {
  const res = await send("POST", "/api/v1/tokens", "Create CLI token", {
    grants: scopes,
  });
  return (await res.json()) as MintedMemberToken;
}

export async function revokeTeamToken(prefix: string): Promise<void> {
  await send("DELETE", `/api/v1/tokens/${seg(prefix)}`, "Revoke token");
}

// The page collects one free-form field; the controller validates each pattern.
export function parseRepoPatterns(input: string): string[] {
  return input
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
}

export function runnerConnectCommand(
  minted: Pick<MintedMachineToken, "capability" | "grants">,
  name: string,
  repos: string[],
  controllerURL: string,
): string {
  const quoted = (s: string) =>
    /^[A-Za-z0-9._:/@=-]+$/.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`;
  const dir = '"$HOME/.config/sparkwing/runner-credentials"';
  return [
    `(umask 077 && mkdir -p ${dir} && rm -f ${dir}/agent-token && printf '%s' ${quoted(minted.capability)} > ${dir}/agent-token) &&`,
    `sparkwing-runner runner --credentials-dir ${dir}`,
    `--controller ${quoted(controllerURL)}`,
    ...repos.map((r) => `--allow-repo '${r.replace(/'/g, `'\\''`)}'`),
    ...(minted.grants.includes("triggers.claim") ? ["--also-claim-triggers"] : []),
    ...(!minted.grants.includes("nodes.claim") ? ["--claim-nodes=false"] : []),
    "--max-claims-before-restart 0 --metrics-addr=",
    `--holder-prefix ${quoted(name)}`,
  ].join(" ");
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
