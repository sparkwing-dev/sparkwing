import { authFetch } from "./api";

// Every price and unit conversion on the billing page comes from here, and
// every figure is derived from the controller's billing response, so a
// pricing change on the controller needs no dashboard change.

export interface RateRow {
  cores: number;
  micro_per_second: number;
}

export interface UsageRow {
  run_id: string;
  seconds: number;
  amount_micro: number;
  // Unix seconds.
  last_charged_at: number;
}

export type GrantKind = "paid" | "free" | "reversal";

export interface GrantRow {
  id: string;
  kind: GrantKind | string;
  amount_micro: number;
  reference?: string;
  // Unix seconds.
  created_at: number;
}

export interface Billing {
  team: string;
  balance_micro: number;
  balance_cap_micro: number;
  micro_per_credit: number;
  credits_per_dollar: number;
  min_billable_seconds: number;
  purchase_min_cents: number;
  purchase_max_cents: number;
  rate_table: RateRow[];
  storage_rate_micro_per_gb_day: number;
  storage_free_allowance_bytes: number;
  checkout_enabled: boolean;
  can_purchase: boolean;
  usage: UsageRow[];
  storage_charged_micro: number;
  grants: GrantRow[];
}

// Units carries only the conversion factors, so the pure helpers can be fed a
// whole Billing or a hand-built fixture.
export type Units = Pick<Billing, "micro_per_credit" | "credits_per_dollar">;

const intFmt = new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 });

function microPerDollar(u: Units): number {
  return u.micro_per_credit * u.credits_per_dollar;
}

function microPerCent(u: Units): number {
  return microPerDollar(u) / 100;
}

// A balance is never shown larger than it is, so partial credits and cents
// round toward zero.
export function balanceCredits(micro: number, u: Units): number {
  return Math.trunc(micro / u.micro_per_credit);
}

export function balanceUSD(micro: number, u: Units): number {
  return Math.trunc(micro / microPerCent(u)) / 100;
}

// A charge or grant rounds to the nearest whole credit and cent.
export function amountCredits(micro: number, u: Units): number {
  return Math.round(micro / u.micro_per_credit);
}

export function amountUSD(micro: number, u: Units): number {
  return Math.round(micro / microPerCent(u)) / 100;
}

export function fmtCredits(credits: number): string {
  return intFmt.format(credits === 0 ? 0 : credits);
}

// fmtUSD always shows cents, for balances and ledger rows.
export function fmtUSD(dollars: number): string {
  const sign = dollars < 0 ? "-" : "";
  return `${sign}$${Math.abs(dollars).toLocaleString("en-US", {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  })}`;
}

// fmtUSDShort drops ".00" from whole-dollar limits such as the cap.
export function fmtUSDShort(dollars: number): string {
  if (Number.isInteger(Math.round(dollars * 100) / 100)) {
    const sign = dollars < 0 ? "-" : "";
    return `${sign}$${intFmt.format(Math.abs(Math.round(dollars)))}`;
  }
  return fmtUSD(dollars);
}

// fmtRateUSD keeps sub-cent rates readable, e.g. $0.0036 per GB-day.
export function fmtRateUSD(dollars: number): string {
  const abs = Math.abs(dollars);
  const digits = abs !== 0 && abs < 0.01 ? 4 : 2;
  return `$${abs.toLocaleString("en-US", {
    minimumFractionDigits: 2,
    maximumFractionDigits: digits,
  })}`;
}

export function fmtCents(cents: number): string {
  return fmtUSDShort(cents / 100);
}

export interface PriceRow {
  cores: number;
  creditsPerSecond: number;
  creditsPerMinute: number;
  usdPerHour: number;
}

export function priceRows(b: Units & Pick<Billing, "rate_table">): PriceRow[] {
  return [...(b.rate_table ?? [])]
    .sort((x, y) => x.cores - y.cores)
    .map((r) => {
      const creditsPerSecond = r.micro_per_second / b.micro_per_credit;
      return {
        cores: r.cores,
        creditsPerSecond,
        creditsPerMinute: creditsPerSecond * 60,
        usdPerHour: (creditsPerSecond * 3600) / b.credits_per_dollar,
      };
    });
}

// A trimmed decimal for credit rates, which are whole today but need not be.
export function fmtRate(n: number): string {
  return n.toLocaleString("en-US", { maximumFractionDigits: 4 });
}

export interface VcpuHourPrice {
  usd: number;
  // False when classes charge different per-vCPU rates; usd is then the lowest.
  uniform: boolean;
}

export function vcpuHourPrice(
  b: Units & Pick<Billing, "rate_table">,
): VcpuHourPrice | null {
  const perCore = priceRows(b)
    .filter((r) => r.cores > 0)
    .map((r) => r.usdPerHour / r.cores);
  if (perCore.length === 0) return null;
  const lo = Math.min(...perCore);
  const hi = Math.max(...perCore);
  return { usd: lo, uniform: Math.abs(hi - lo) < 1e-9 };
}

// The controller prices storage per gibibyte and calls it a GB.
const bytesPerGB = 2 ** 30;

export interface StoragePrice {
  creditsPerGBDay: number;
  usdPerGBDay: number;
  usdPerGBMonth: number;
  freeGB: number;
}

// Storage is omitted from the page until the controller charges for it.
export function storagePrice(
  b: Units &
    Pick<
      Billing,
      "storage_rate_micro_per_gb_day" | "storage_free_allowance_bytes"
    >,
): StoragePrice | null {
  const rate = b.storage_rate_micro_per_gb_day;
  if (!(rate > 0)) return null;
  const creditsPerGBDay = rate / b.micro_per_credit;
  const usdPerGBDay = creditsPerGBDay / b.credits_per_dollar;
  return {
    creditsPerGBDay,
    usdPerGBDay,
    usdPerGBMonth: usdPerGBDay * 30,
    freeGB: (b.storage_free_allowance_bytes || 0) / bytesPerGB,
  };
}

export function fmtGB(gb: number): string {
  return `${gb.toLocaleString("en-US", { maximumFractionDigits: 2 })} GB`;
}

export function grantLabel(kind: string): string {
  switch (kind) {
    case "paid":
      return "Purchase";
    case "free":
      return "Grant";
    case "reversal":
      return "Refund";
    default:
      return kind;
  }
}

// parseDollars reads the amount field: whole dollars or dollars and cents,
// with an optional "$" and thousands commas. Anything else is null.
export function parseDollars(input: string): number | null {
  const s = input.trim().replace(/^\$/, "").replace(/,/g, "").trim();
  const m = /^(\d+)(?:\.(\d{1,2}))?$/.exec(s);
  if (!m) return null;
  const cents = Number(m[1]) * 100 + Number((m[2] ?? "").padEnd(2, "0"));
  return Number.isSafeInteger(cents) ? cents : null;
}

export function creditsForCents(cents: number, u: Units): number {
  return Math.floor((cents * u.credits_per_dollar) / 100);
}

// purchaseProblem mirrors the controller's checks so the owner hears about a
// bad amount before leaving for Stripe; the controller checks again.
export function purchaseProblem(
  cents: number | null,
  b: Units &
    Pick<
      Billing,
      | "purchase_min_cents"
      | "purchase_max_cents"
      | "balance_micro"
      | "balance_cap_micro"
    >,
): string | null {
  const range = `Enter an amount from ${fmtCents(b.purchase_min_cents)} to ${fmtCents(b.purchase_max_cents)}.`;
  if (cents === null) return range;
  if (cents < b.purchase_min_cents || cents > b.purchase_max_cents) {
    return range;
  }
  if (b.balance_micro + cents * microPerCent(b) > b.balance_cap_micro) {
    return capMessage(b.balance_micro, b.balance_cap_micro, b);
  }
  return null;
}

function capMessage(
  balanceMicro: number,
  capMicro: number,
  u: Units,
  openMicro = 0,
): string {
  const room = Math.max(0, capMicro - balanceMicro - openMicro);
  const roomCents = Math.floor(room / microPerCent(u));
  const open =
    openMicro > 0
      ? ` and ${fmtUSD(balanceUSD(openMicro, u))} in checkouts still open`
      : "";
  return (
    `A team balance is capped at ${fmtUSDShort(balanceUSD(capMicro, u))}. ` +
    `The balance is ${fmtUSD(balanceUSD(balanceMicro, u))} ` +
    `(${fmtCredits(balanceCredits(balanceMicro, u))} credits)${open}, so this team can buy ` +
    `up to ${fmtUSD(roomCents / 100)} more.`
  );
}

export class BillingApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
    readonly code?: string,
  ) {
    super(message);
  }
}

interface ErrorBody {
  error?: string;
  message?: string;
  code?: string;
  balance_micro?: number;
  open_micro?: number;
  cap_micro?: number;
  amount_micro?: number;
}

async function readErrorBody(res: Response): Promise<ErrorBody> {
  try {
    const text = (await res.text()).trim();
    try {
      const body = JSON.parse(text) as unknown;
      if (body && typeof body === "object") return body as ErrorBody;
    } catch {
      // A proxy or gateway may answer in plain text.
    }
    return text ? { error: text } : {};
  } catch {
    return {};
  }
}

// checkoutErrorMessage turns a refused checkout into a sentence the owner can
// act on. Units come from the loaded billing so the cap reads in dollars.
export function checkoutErrorMessage(
  status: number,
  body: ErrorBody,
  u: Units,
): string {
  const detail = body.error || body.message || "";
  if (status === 409 && body.code === "balance_cap") {
    if (
      typeof body.balance_micro === "number" &&
      typeof body.cap_micro === "number"
    ) {
      return capMessage(
        body.balance_micro,
        body.cap_micro,
        u,
        typeof body.open_micro === "number" ? body.open_micro : 0,
      );
    }
    return detail || "This purchase would take the balance over its cap.";
  }
  if (status === 400) {
    return detail
      ? `The controller refused this amount: ${detail}`
      : "The controller refused this amount.";
  }
  if (status === 403) {
    return "Only a team owner can buy credits.";
  }
  if (status === 503) {
    return "Buying credits is not available on this controller right now.";
  }
  if (status === 502) {
    return "The payment service did not start a checkout. Nothing was charged; try again in a moment.";
  }
  return detail ? `Checkout failed: ${detail}` : `Checkout failed (${status}).`;
}

function normalize(body: Partial<Billing>): Billing {
  return {
    ...(body as Billing),
    rate_table: body.rate_table ?? [],
    usage: body.usage ?? [],
    grants: body.grants ?? [],
    storage_rate_micro_per_gb_day: body.storage_rate_micro_per_gb_day ?? 0,
    storage_free_allowance_bytes: body.storage_free_allowance_bytes ?? 0,
    checkout_enabled: body.checkout_enabled === true,
    can_purchase: body.can_purchase === true,
  };
}

export async function getBilling(): Promise<Billing> {
  const res = await authFetch("/api/v1/team/billing");
  if (!res.ok) {
    const body = await readErrorBody(res);
    const detail = body.error || body.message;
    throw new BillingApiError(
      res.status,
      detail
        ? `Load billing: ${detail}`
        : `Load billing failed (${res.status})`,
      body.code,
    );
  }
  return normalize((await res.json()) as Partial<Billing>);
}

// startCheckout names only the amount; the controller bills the session's
// active team, so the request never carries one.
export async function startCheckout(
  amountCents: number,
  u: Units,
): Promise<string> {
  const res = await authFetch("/api/v1/team/billing/checkout", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ amount_cents: amountCents }),
  });
  if (!res.ok) {
    const body = await readErrorBody(res);
    throw new BillingApiError(
      res.status,
      checkoutErrorMessage(res.status, body, u),
      body.code,
    );
  }
  const body = (await res.json()) as { url?: unknown };
  if (typeof body.url !== "string" || !/^https?:\/\//.test(body.url)) {
    throw new BillingApiError(
      res.status,
      "The controller did not return a checkout link.",
    );
  }
  return body.url;
}

export type CheckoutReturn = "success" | "cancelled" | null;

export function checkoutReturn(param: string | null): CheckoutReturn {
  return param === "success" || param === "cancelled" ? param : null;
}

// After Stripe sends the owner back, the webhook that grants the credits may
// land a few seconds later; the page reloads billing at these offsets (ms).
export const checkoutRefreshDelays = [2_000, 5_000, 10_000, 15_000, 20_000];
