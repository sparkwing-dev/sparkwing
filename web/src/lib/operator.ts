import { authFetch } from "./api";
import {
  type StandingHistory,
  type StandingRequest,
  fmtCents,
  parseDollars,
} from "./billing";

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
  subject_id?: string;
  run_id?: string;
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
    standing: string;
    cloud_vcpu: number;
    max_job_seconds: number;
    trust_by?: string;
    // Unix seconds.
    trust_at?: number;
    trust_reason?: string;
  };
  frozen: boolean;
  holds: string[];
  // hold_causes[i] names why holds[i] was placed.
  hold_causes: HoldCause[];
  events: OperatorEvent[];
  next_cursor?: string;
  // Queued refunds Stripe failed or canceled; return the money by hand in Stripe.
  failed_refunds: FailedRefund[];
}

export interface FailedRefund {
  payment_intent: string;
  refund_id: string;
  status: string;
  reason: string;
  amount_micro: number;
  // Unix seconds.
  created_at: number;
}

export type HoldCause = "operator" | "dispute" | "early_fraud_warning" | string;

const holdCauseLabels: Record<string, string> = {
  dispute: "payment dispute",
  early_fraud_warning: "early fraud warning",
};

// holdLabel names a payment hold by the cause the controller recorded.
export function holdLabel(team: OperatorTeam, hold: string): string {
  const cause = team.hold_causes[team.holds.indexOf(hold)] ?? "";
  return `${holdCauseLabels[cause] ?? "payment hold"} ${hold}`;
}

export type ActionKind =
  | "grant-trust"
  | "revoke-trust"
  | "reset-trust"
  | "grant-credits"
  | "freeze"
  | "unfreeze"
  | "release-hold";

export const actionLabels: Record<ActionKind, string> = {
  "grant-trust": "Trust",
  "revoke-trust": "Revoke trust",
  "reset-trust": "Reset to automatic",
  "grant-credits": "Grant credits",
  freeze: "Freeze",
  unfreeze: "Unfreeze",
  "release-hold": "Release a payment hold",
};

export interface ActionExtra {
  hold?: string;
  noFault?: boolean;
}



// A balance in micro-credits as whole cents, never rounded up: a cent is
// 1,000,000 micro-credits.
export function balanceCents(micro: number): number {
  return Math.trunc(micro / 1_000_000);
}

export function needsAmount(kind: ActionKind): boolean {
  return kind === "grant-credits";
}

// operatorReasonLimit counts Unicode code points, as the controller does.
export const operatorReasonLimit = 500;

// actionProblem says why the action cannot be reviewed yet, or "" when it can.
export function actionProblem(
  kind: ActionKind,
  reason: string,
  amount: string,
  extra: ActionExtra = {},
): string {
  if (needsAmount(kind)) {
    const cents = parseDollars(amount);
    if (cents === null || cents <= 0) {
      return "Enter a positive dollar amount.";
    }
  }
  if (reason.trim() === "")
    return "Give a reason; it is recorded with the action.";
  if ([...reason.trim()].length > operatorReasonLimit)
    return `Keep the reason to ${operatorReasonLimit} characters.`;
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
      return `${name} receives High standing unless a payment hold is active.`;
    case "revoke-trust":
      return `${name} moves to Held. New Cloud work waits while running work finishes.`;
    case "reset-trust":
      return `${name} returns to standing based on payment history and prepaid funds.`;
    case "grant-credits":
      return `${name} receives ${fmtCents(cents)} of free credit.`;
    case "freeze":
      return `${name}'s cloud runs stop starting until it is unfrozen, and running work finishes.`;
    case "unfreeze": {
      const payment = paymentHolds(team).length;
      const fault = extra.noFault ? " as no fault found, so they do not restart the clean-day count" : "";
      return `The ${operatorHolds(team).length} operator hold(s) on ${name} are released${fault}.${payment ? ` ${payment} payment hold(s) stay, so it stays frozen.` : " Its cloud runs start again."}`;
    }
    case "release-hold": {
      const left = team.holds.filter((h) => h !== extra.hold).length;
      return `The ${holdLabel(team, extra.hold ?? "")} on ${name} is released.${left ? ` ${left} other hold(s) stay, so it stays frozen.` : " Its cloud runs start again."}`;
    }
  }
}

// paymentHolds are the holds a dispute or fraud warning placed.
export function paymentHolds(team: OperatorTeam): string[] {
  return team.holds.filter((h, i) => team.hold_causes[i] !== "operator");
}

// operatorHolds are the holds the console placed.
export function operatorHolds(team: OperatorTeam): string[] {
  return team.holds.filter((h, i) => team.hold_causes[i] === "operator");
}

export function availableActions(team: OperatorTeam): ActionKind[] {
  const b = team.billing;
  const out: ActionKind[] = [];
  if (b.trust !== "granted") out.push("grant-trust");
  if (b.trust !== "revoked") out.push("revoke-trust");
  if (b.trust !== "automatic") out.push("reset-trust");
  out.push("grant-credits", operatorHolds(team).length ? "unfreeze" : "freeze");
  if (paymentHolds(team).length) out.push("release-hold");
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
    case "grant-credits":
      return {
        path: `${base}/grants`,
        body: { amount_cents: cents, reason, key },
      };
    case "freeze":
      return { path: `${base}/freeze`, body: { reason } };
    case "unfreeze":
      return { path: `${base}/unfreeze`, body: extra.noFault ? { reason, no_fault: true } : { reason } };
    case "release-hold":
      return { path: `${base}/unfreeze`, body: { reason, hold: extra.hold } };
  }
}

async function readJSON<T>(res: Response): Promise<T> {
  if (res.ok) return (await res.json()) as T;
  let message = `${res.status} ${res.statusText}`;
  try {
    const body = (await res.json()) as { error?: { message?: string } };
    message = body.error?.message || message;
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

export async function getOperatorTeam(team: string, cursor?: string): Promise<OperatorTeam> {
  const query = cursor ? `?cursor=${encodeURIComponent(cursor)}` : "";
  const res = await authFetch(
    `/api/v1/operator/teams/${encodeURIComponent(team)}${query}`,
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
      headers: { "Content-Type": "application/json", "Idempotency-Key": crypto.randomUUID() },
      body: JSON.stringify({ account_ids: ids }),
    },
    quiet,
  );
  const body = await readJSON<{ approved: { email: string }[] }>(res);
  return body.approved.map((a) => a.email);
}

export interface OperatorStandingRequest {
  request: StandingRequest;
  display_name: string;
  standing: string;
  prepay_ceiling: string;
  balance_micro: number;
  history: StandingHistory;
}

export async function getStandingRequests(): Promise<OperatorStandingRequest[]> {
  const res = await authFetch("/api/v1/operator/standing-requests", {}, quiet);
  return (await readJSON<{ requests: OperatorStandingRequest[] }>(res)).requests;
}

// approveStandingRequest grants standing and approves the reviewed request,
// which the controller refuses once that request is no longer open.
export async function approveStandingRequest(team: string, request: string, reason: string): Promise<void> {
  const grant = actionRequest(team, "grant-trust", reason, "", "");
  await performAction({ path: grant.path, body: { ...grant.body, standing_request: request } });
}

export async function declineStandingRequest(team: string, request: string, reason: string): Promise<void> {
  const res = await authFetch(
    `/api/v1/operator/teams/${encodeURIComponent(team)}/standing-request/decline`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ request, reason: reason.trim() }),
    },
    quiet,
  );
  await readJSON<unknown>(res);
}

const dayMs = 24 * 60 * 60 * 1000;

// historyFacts summarizes the payment record a team's history tier comes from.
export function historyFacts(h: StandingHistory, now = Date.now()): string {
  const parts = [`${h.tier} history`, `${fmtCents(balanceCents(h.paid_micro))} paid`];
  if (h.first_paid_at) {
    const since = Math.max(h.first_paid_at, h.last_incident_at ?? 0) * 1000;
    parts.push(`${Math.max(Math.floor((now - since) / dayMs), 0)} clean days`);
  } else {
    parts.push("no payments");
  }
  return parts.join(" · ");
}
