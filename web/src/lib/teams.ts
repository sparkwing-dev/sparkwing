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

export async function getMe(): Promise<Me | null> {
  try {
    const res = await authFetch("/api/v1/me");
    if (!res.ok) return null;
    return (await res.json()) as Me;
  } catch {
    return null;
  }
}

export async function switchTeam(slug: string): Promise<void> {
  await send("POST", "/api/v1/me/active-team", "Switch team", { slug });
}
