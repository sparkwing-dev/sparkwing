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
    daily_cap_cents?: number;
    rung_cents?: number;
    runner_cap?: number;
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
  | "unfreeze"
  | "release-hold";

export const actionLabels: Record<ActionKind, string> = {
  "grant-trust": "Trust",
  "revoke-trust": "Revoke trust",
  "reset-trust": "Reset to automatic",
  "set-limit": "Set limit override",
  "clear-limit": "Clear limit override",
  "grant-credits": "Grant credits",
  freeze: "Freeze",
  unfreeze: "Unfreeze",
  "release-hold": "Release a dispute hold",
};

// ActionExtra carries the inputs only some actions take: the optional
// overrides of set-limit, as typed, and the hold release-hold names.
export interface ActionExtra {
  daily?: string;
  rung?: string;
  runners?: string;
  hold?: string;
}

export const maxRunnerCap = 10_000;

export const maxOperatorCents = 500_000;

// A balance in micro-credits as whole cents, never rounded up: a cent is
// 1,000,000 micro-credits.
export function balanceCents(micro: number): number {
  return Math.trunc(micro / 1_000_000);
}

export function needsAmount(kind: ActionKind): boolean {
  return kind === "set-limit" || kind === "grant-credits";
}

function optionalCents(raw: string | undefined): number | null {
  if (!raw?.trim()) return 0;
  const cents = parseDollars(raw);
  return cents === null || cents <= 0 || cents > maxOperatorCents ? null : cents;
}

function optionalRunners(raw: string | undefined): number | null {
  if (!raw?.trim()) return 0;
  const n = Number(raw.trim());
  return Number.isInteger(n) && n > 0 && n <= maxRunnerCap ? n : null;
}

// actionProblem says why the action cannot be reviewed yet, or "" when it can.
export function actionProblem(
  kind: ActionKind,
  reason: string,
  amount: string,
  extra: ActionExtra = {},
): string {
  if (needsAmount(kind)) {
    const cents = parseDollars(amount);
    if (cents === null || cents <= 0 || cents > maxOperatorCents) {
      return `Enter an amount from $0.01 to ${fmtCents(maxOperatorCents)}.`;
    }
  }
  if (kind === "set-limit") {
    if (optionalCents(extra.daily) === null || optionalCents(extra.rung) === null) {
      return `Leave a daily cap or rung empty, or enter $0.01 to ${fmtCents(maxOperatorCents)}.`;
    }
    if (optionalRunners(extra.runners) === null) {
      return `Leave the node cap empty, or enter a whole number from 1 to ${maxRunnerCap.toLocaleString("en-US")}.`;
    }
  }
  if (reason.trim() === "")
    return "Give a reason; it is recorded with the action.";
  if (reason.trim().length > 500) return "Keep the reason to 500 characters.";
  if (kind === "release-hold" && !extra.hold) return "Choose the hold to release.";
  return "";
}

// actionEffect states, for the confirmation step, what the action does to team.
export function actionEffect(
  team: OperatorTeam,
  kind: ActionKind,
  amount: string,
  extra: ActionExtra = {},
): string {
  const cents = parseDollars(amount) ?? 0;
  const name = team.display_name || team.team;
  switch (kind) {
    case "grant-trust":
      return `${name} becomes trusted and may buy ${fmtCents(50_000)} every 30 days, whatever the automatic rule says. Every override is cleared.`;
    case "revoke-trust":
      return `${name} is held to the new-team limit of ${fmtCents(5_000)} every 30 days, whatever the automatic rule says. Every override is cleared.`;
    case "reset-trust":
      return `${name} returns to the automatic rule and every override is cleared.`;
    case "set-limit": {
      const parts = [`may buy ${fmtCents(cents)} every 30 days`];
      const daily = optionalCents(extra.daily);
      const rung = optionalCents(extra.rung);
      const runners = optionalRunners(extra.runners);
      if (daily) parts.push(`spends up to ${fmtCents(daily)} a day`);
      if (rung) parts.push(`has its card charged at ${fmtCents(rung)} of debt`);
      if (runners) parts.push(`holds up to ${runners} Cloud nodes at once`);
      return `${name} becomes trusted and ${parts.join(", ")}. Every override left empty returns to its default.`;
    }
    case "clear-limit":
      return `${name} stays trusted and returns to the trusted limit of ${fmtCents(50_000)} every 30 days, and every other override returns to its default.`;
    case "grant-credits":
      return `${name} receives ${fmtCents(cents)} of free credit.`;
    case "freeze":
      return `${name}'s cloud runs stop starting until it is unfrozen, and it loses automatic trust for good.`;
    case "unfreeze": {
      const disputes = team.holds.length - operatorHolds(team).length;
      return `The ${operatorHolds(team).length} operator hold(s) on ${name} are released.${disputes ? ` ${disputes} dispute hold(s) stay, so it stays frozen.` : " Its cloud runs start again."}`;
    }
    case "release-hold": {
      const left = team.holds.filter((h) => h !== extra.hold).length;
      return `The dispute hold ${extra.hold} on ${name} is released.${left ? ` ${left} other hold(s) stay, so it stays frozen.` : " Its cloud runs start again."}`;
    }
  }
}

// disputeHolds are the holds a payment dispute placed, which the console
// releases one at a time once the dispute is settled.
export function disputeHolds(team: OperatorTeam): string[] {
  return team.holds.filter((h) => !h.startsWith("operator-"));
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
  if (disputeHolds(team).length) out.push("release-hold");
  return out;
}

// actionRequest is the controller call the confirmed action makes.
export function actionRequest(
  team: string,
  kind: ActionKind,
  reason: string,
  amount: string,
  key: string,
  extra: ActionExtra = {},
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
    case "set-limit": {
      const body: Record<string, unknown> = {
        trust: "granted",
        reason,
        limit_cents: cents,
      };
      const daily = optionalCents(extra.daily);
      const rung = optionalCents(extra.rung);
      const runners = optionalRunners(extra.runners);
      if (daily) body.daily_cap_cents = daily;
      if (rung) body.rung_cents = rung;
      if (runners) body.runner_cap = runners;
      return { path: `${base}/trust`, body };
    }
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
    case "release-hold":
      return { path: `${base}/unfreeze`, body: { reason, hold: extra.hold } };
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

export interface WaitlistEntry {
  id: string;
  email: string;
  name: string;
  provider: string;
  reason: string;
  // Unix seconds.
  waitlisted_at: number;
}

export interface WaitlistPage {
  accounts: WaitlistEntry[];
  total: number;
}

export const waitlistPageSize = 50;

// getWaitlist reads the page of waiting accounts that starts offset accounts
// after the newest.
export async function getWaitlist(
  offset: number,
  limit = waitlistPageSize,
): Promise<WaitlistPage> {
  const res = await authFetch(
    `/api/v1/operator/waitlist?limit=${limit}&offset=${offset}`,
    {},
    quiet,
  );
  return readJSON<WaitlistPage>(res);
}

// approveWaitlisted admits the accounts and returns the emails it admitted;
// an account already admitted is skipped.
export async function approveWaitlisted(ids: string[]): Promise<string[]> {
  const res = await authFetch(
    "/api/v1/operator/waitlist/approve",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ account_ids: ids }),
    },
    quiet,
  );
  const body = await readJSON<{ approved: { email: string }[] }>(res);
  return body.approved.map((a) => a.email);
}
