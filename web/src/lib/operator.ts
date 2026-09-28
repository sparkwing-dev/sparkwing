import { authFetch } from "./api";
import { fmtCents, parseDollars } from "./billing";

// The operator console's reads and writes. The controller answers them only
// for a listed operator's own session, so nothing here decides access.

export interface TeamMatch {
  team: string;
  display_name: string;
  owners: string[];
}

export interface OperatorEvent {
  // Unix seconds.
  at: number;
  kind: string;
  actor?: string;
  attrs?: Record<string, unknown>;
}

export interface OperatorTeam {
  team: string;
  display_name: string;
  owners: string[];
  balance_micro: number;
  billing: {
    trust: "automatic" | "granted" | "revoked";
    trusted: boolean;
    trust_by?: string;
    // Unix seconds.
    trust_at?: number;
    trust_reason?: string;
    limit_override_cents?: number;
    purchase_limit_cents: number;
    purchased_30d_cents: number;
  };
  frozen: boolean;
  holds: string[];
  events: OperatorEvent[];
}

export type ActionKind =
  | "grant-trust"
  | "revoke-trust"
  | "reset-trust"
  | "set-limit"
  | "clear-limit"
  | "grant-credits"
  | "freeze"
  | "unfreeze";

export const actionLabels: Record<ActionKind, string> = {
  "grant-trust": "Trust",
  "revoke-trust": "Revoke trust",
  "reset-trust": "Reset to automatic",
  "set-limit": "Set limit override",
  "clear-limit": "Clear limit override",
  "grant-credits": "Grant credits",
  freeze: "Freeze",
  unfreeze: "Unfreeze",
};

export const maxOperatorCents = 500_000;

// A balance in micro-credits as whole cents, never rounded up: a cent is
// 1,000,000 micro-credits.
export function balanceCents(micro: number): number {
  return Math.trunc(micro / 1_000_000);
}

export function needsAmount(kind: ActionKind): boolean {
  return kind === "set-limit" || kind === "grant-credits";
}

// actionProblem says why the action cannot be reviewed yet, or "" when it can.
export function actionProblem(
  kind: ActionKind,
  reason: string,
  amount: string,
): string {
  if (needsAmount(kind)) {
    const cents = parseDollars(amount);
    if (cents === null || cents <= 0 || cents > maxOperatorCents) {
      return `Enter an amount from $0.01 to ${fmtCents(maxOperatorCents)}.`;
    }
  }
  if (reason.trim() === "")
    return "Give a reason; it is recorded with the action.";
  if (reason.trim().length > 500) return "Keep the reason to 500 characters.";
  return "";
}

// actionEffect states, for the confirmation step, what the action does to team.
export function actionEffect(
  team: OperatorTeam,
  kind: ActionKind,
  amount: string,
): string {
  const cents = parseDollars(amount) ?? 0;
  const name = team.display_name || team.team;
  switch (kind) {
    case "grant-trust":
      return `${name} becomes trusted and may buy ${fmtCents(50_000)} every 30 days, whatever the automatic rule says. Any limit override is cleared.`;
    case "revoke-trust":
      return `${name} is held to the new-team limit of ${fmtCents(5_000)} every 30 days, whatever the automatic rule says. Any limit override is cleared.`;
    case "reset-trust":
      return `${name} returns to the automatic rule and any limit override is cleared.`;
    case "set-limit":
      return `${name} becomes trusted and may buy ${fmtCents(cents)} every 30 days.`;
    case "clear-limit":
      return `${name} stays trusted and returns to the trusted limit of ${fmtCents(50_000)} every 30 days.`;
    case "grant-credits":
      return `${name} receives ${fmtCents(cents)} of free credit.`;
    case "freeze":
      return `${name}'s cloud runs stop starting until it is unfrozen, and it loses automatic trust for good.`;
    case "unfreeze": {
      const disputes = team.holds.length - operatorHolds(team).length;
      return `The ${operatorHolds(team).length} operator hold(s) on ${name} are released.${disputes ? ` ${disputes} dispute hold(s) stay, so it stays frozen.` : " Its cloud runs start again."}`;
    }
  }
}

// operatorHolds are the holds the console placed; a payment dispute's hold is
// released only outside it.
export function operatorHolds(team: OperatorTeam): string[] {
  return team.holds.filter((h) => h.startsWith("operator-"));
}

// availableActions offers no limit on a revoked team, because raising a limit
// must never restore trust in passing.
export function availableActions(team: OperatorTeam): ActionKind[] {
  const b = team.billing;
  const out: ActionKind[] = [];
  if (b.trust !== "granted") out.push("grant-trust");
  if (b.trust !== "revoked") out.push("revoke-trust", "set-limit");
  if (b.trust !== "automatic") out.push("reset-trust");
  if (b.limit_override_cents) out.push("clear-limit");
  out.push("grant-credits", operatorHolds(team).length ? "unfreeze" : "freeze");
  return out;
}

// actionRequest is the controller call the confirmed action makes.
export function actionRequest(
  team: string,
  kind: ActionKind,
  reason: string,
  amount: string,
  key: string,
): { path: string; body: Record<string, unknown> } {
  const base = `/api/v1/operator/teams/${encodeURIComponent(team)}`;
  const cents = parseDollars(amount) ?? 0;
  reason = reason.trim();
  switch (kind) {
    case "grant-trust":
      return { path: `${base}/trust`, body: { trust: "granted", reason } };
    case "revoke-trust":
      return { path: `${base}/trust`, body: { trust: "revoked", reason } };
    case "reset-trust":
      return { path: `${base}/trust`, body: { trust: "automatic", reason } };
    case "set-limit":
      return {
        path: `${base}/trust`,
        body: { trust: "granted", reason, limit_cents: cents },
      };
    case "clear-limit":
      return {
        path: `${base}/trust`,
        body: { trust: "granted", reason, limit_cents: 0 },
      };
    case "grant-credits":
      return {
        path: `${base}/grants`,
        body: { amount_cents: cents, reason, key },
      };
    case "freeze":
      return { path: `${base}/freeze`, body: { reason } };
    case "unfreeze":
      return { path: `${base}/unfreeze`, body: { reason } };
  }
}

async function readJSON<T>(res: Response): Promise<T> {
  if (res.ok) return (await res.json()) as T;
  let message = `${res.status} ${res.statusText}`;
  try {
    const body = (await res.json()) as { message?: string; error?: string };
    message = body.message || body.error || message;
  } catch {}
  throw new Error(message);
}

const quiet = { speaksForSession: false };

export async function searchTeams(q: string): Promise<TeamMatch[]> {
  const res = await authFetch(
    `/api/v1/operator/teams?q=${encodeURIComponent(q)}`,
    {},
    quiet,
  );
  return (await readJSON<{ teams: TeamMatch[] }>(res)).teams;
}

export async function getOperatorTeam(team: string): Promise<OperatorTeam> {
  const res = await authFetch(
    `/api/v1/operator/teams/${encodeURIComponent(team)}`,
    {},
    quiet,
  );
  return readJSON<OperatorTeam>(res);
}

export async function performAction(req: {
  path: string;
  body: Record<string, unknown>;
}): Promise<void> {
  const res = await authFetch(
    req.path,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(req.body),
    },
    quiet,
  );
  await readJSON<unknown>(res);
}
