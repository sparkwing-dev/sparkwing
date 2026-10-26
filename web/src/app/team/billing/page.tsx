"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useCallback, useEffect, useState } from "react";
import TeamShell, {
  Panel,
  Notice,
  buttonClass,
  errorText,
  inputClass,
} from "@/components/TeamShell";
import {
  type Billing,
  amountCredits,
  amountUSD,
  balanceCredits,
  balanceUSD,
  checkoutRefreshDelays,
  checkoutReturn,
  creditsForCents,
  fmtCents,
  fmtCredits,
  fmtGB,
  fmtRate,
  fmtRateUSD,
  fmtUSD,
  getBilling,
  grantLabel,
  holdNotice,
  openCardPage,
  parseDollars,
  prepayNote,
  priceRows,
  requestHighStanding,
  standingOutcome,
  standingRequestProblem,
  purchaseProblem,
  refillProblem,
  refillStatus,
  setRefill,
  startCheckout,
  suggestedRefill,
  storagePrice,
} from "@/lib/billing";
import { billingEnabled, unixSecondsISO } from "@/lib/teams";
import { fmtDateTime } from "@/lib/timeFormat";

export default function BillingPage() {
  return (
    <Suspense>
      <TeamShell>
        {(_, caps) =>
          billingEnabled(caps) ? (
            <BillingRoute />
          ) : (
            <Notice>Billing is not enabled on this controller.</Notice>
          )
        }
      </TeamShell>
    </Suspense>
  );
}

const labelClass =
  "block text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1";
const thClass =
  "px-4 py-2 text-left text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]";
const tdClass = "px-4 py-2";

function BillingRoute() {
  const params = useSearchParams();
  const returned = checkoutReturn(params.get("checkout"));
  const cardReturn = params.get("card");
  const [billing, setBilling] = useState<Billing | null>(null);
  const [loadError, setLoadError] = useState("");

  const load = useCallback(async () => {
    try {
      setBilling(await getBilling());
      setLoadError("");
    } catch (err) {
      setLoadError(errorText(err));
    }
  }, []);

  useEffect(() => {
    const delays = returned === "success" ? [0, ...checkoutRefreshDelays] : [0];
    const timers = delays.map((ms) => setTimeout(load, ms));
    return () => timers.forEach(clearTimeout);
  }, [returned, load]);

  return (
    <>
      {returned === "success" ? (
        <div
          role="status"
          className="mb-6 rounded-lg border border-green-500/30 bg-green-500/10 px-4 py-3 text-sm text-green-300"
        >
          Payment received. Credits appear once Stripe confirms the payment.
        </div>
      ) : returned === "cancelled" ? (
        <div
          role="status"
          className="mb-6 rounded-lg border border-[var(--border)] bg-[var(--surface)] px-4 py-3 text-sm text-[var(--muted)]"
        >
          Checkout was cancelled. Nothing was charged.
        </div>
      ) : null}
      {loadError && billing === null ? (
        <Panel title="Billing">
          <div className="p-4 text-sm text-red-300">{loadError}</div>
        </Panel>
      ) : billing === null ? (
        <Panel title="Billing">
          <div className="p-4 text-xs text-[var(--muted)]">Loading…</div>
        </Panel>
      ) : (
        <>
          {billing.frozen || billing.hold_cause ? (
            <div
              role="alert"
              className="mb-6 rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-300"
            >
              {holdNotice(billing.hold_cause)}
            </div>
          ) : billing.balance_micro <= 0 ? (
            <div
              role="alert"
              className="mb-6 rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-300"
            >
              The balance is empty, so Cloud work cannot run. Add funds or turn
              on automatic refill; your own runners keep working.
            </div>
          ) : null}
          {cardReturn === "added" ? (
            <Notice>Card added. It is saved once Stripe confirms it.</Notice>
          ) : null}
          <BalancePanel billing={billing} />
          <CardPanel billing={billing} />
          <RefillPanel billing={billing} reload={load} />
          <StandingPanel billing={billing} reload={load} />
          {billing.checkout_enabled ? (
            <BuyPanel billing={billing} />
          ) : null}
          <PricesPanel billing={billing} />
          <UsagePanel billing={billing} />
          <HistoryPanel billing={billing} />
        </>
      )}
    </>
  );
}

function BalancePanel({ billing }: { billing: Billing }) {
  const credits = balanceCredits(billing.balance_micro, billing);
  return (
    <Panel title="Balance">
      <div className="p-4">
        <div className="text-2xl font-bold">
          {fmtCredits(credits)}{" "}
          <span className="text-sm font-normal text-[var(--muted)]">
            {credits === 1 ? "credit" : "credits"}
          </span>
        </div>
        <div className="text-sm text-[var(--muted)] mt-1">
          {fmtUSD(balanceUSD(billing.balance_micro, billing))} prepaid
        </div>
      </div>
    </Panel>
  );
}

function BuyPanel({ billing }: { billing: Billing }) {
  const [amount, setAmount] = useState(() =>
    String(billing.purchase_min_cents / 100),
  );
  const [busy, setBusy] = useState(false);
  const [submitError, setSubmitError] = useState("");

  const cents = parseDollars(amount);
  const problem = purchaseProblem(cents, billing);
  const owner = billing.can_purchase;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!owner || cents === null || problem) return;
    setBusy(true);
    setSubmitError("");
    try {
      window.location.assign(await startCheckout(cents));
    } catch (err) {
      setSubmitError(errorText(err));
      setBusy(false);
    }
  }

  return (
    <Panel
      title="Add funds"
      hint={`Minimum purchase ${fmtCents(billing.purchase_min_cents)}. Cloud usage draws on the balance.`}
    >
      <form onSubmit={submit} className="p-4 space-y-2">
        {owner ? null : (
          <p className="text-sm text-[var(--muted)]">
            Only a team owner can buy credits.
          </p>
        )}
        <fieldset disabled={!owner || busy} className="space-y-2">
          <label htmlFor="buy-amount" className={labelClass}>
            Amount in US dollars
          </label>
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm text-[var(--muted)]" aria-hidden="true">
              $
            </span>
            <input
              id="buy-amount"
              inputMode="decimal"
              autoComplete="off"
              aria-invalid={problem ? true : undefined}
              aria-describedby="buy-amount-note"
              className={`${inputClass} w-32`}
              value={amount}
              onChange={(e) => {
                setAmount(e.target.value);
                setSubmitError("");
              }}
            />
            <button
              type="submit"
              className={buttonClass}
              disabled={problem !== null}
            >
              {busy ? "Opening checkout…" : "Add funds"}
            </button>
          </div>
        </fieldset>
        <p
          id="buy-amount-note"
          className={`text-xs ${problem && owner ? "text-red-300" : "text-[var(--muted)]"}`}
        >
          {problem && owner
            ? problem
            : cents !== null && !problem
              ? `Buys ${fmtCredits(creditsForCents(cents, billing))} credits.`
              : null}
        </p>
        {submitError ? (
          <p role="alert" className="text-sm text-red-300">
            {submitError}
          </p>
        ) : null}
        <p className="text-xs text-[var(--muted)]">
          {billing.purchase_max_cents > 0 ? `Each payment is at most ${fmtCents(billing.purchase_max_cents)}.` : "Payment limits are unavailable. Reload billing."}{" "}
          <a href="#standing-message" className="underline">Contact us for larger payments.</a>{" "}
        </p>
        <p className="text-xs text-[var(--muted)]">
          Purchases are final. Credits never expire.
        </p>
      </form>
    </Panel>
  );
}

function PricesPanel({ billing }: { billing: Billing }) {
  const rows = priceRows(billing);
  const storage = storagePrice(billing);
  return (
    <Panel title="Prices">
      {rows.length === 0 ? (
        <div className="p-4 text-xs text-[var(--muted)]">
          The controller reported no machine classes.
        </div>
      ) : (
        <table className="w-full text-sm">
          <thead className="border-b border-[var(--border)]">
            <tr>
              <th scope="col" className={thClass}>
                Machine
              </th>
              <th scope="col" className={`${thClass} text-right`}>
                Credits / second
              </th>
              <th scope="col" className={`${thClass} text-right`}>
                Per hour
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr
                key={r.cores}
                className="border-b border-[var(--border)] last:border-0"
              >
                <td className={tdClass}>{r.cores} vCPU</td>
                <td className={`${tdClass} text-right font-mono`}>
                  {fmtRate(r.creditsPerSecond)}
                </td>
                <td className={`${tdClass} text-right font-mono`}>
                  {fmtRateUSD(r.usdPerHour)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <div className="px-4 py-3 border-t border-[var(--border)] text-xs text-[var(--muted)] space-y-1">
        <p>
          Usage is charged in {billing.slice_seconds}-second slices, each paid
          before it runs; unused slice time is refunded. {fmtCredits(billing.credits_per_dollar)} credits cost $1.
        </p>
        {storage ? (
          <p>
            Storage: {fmtRate(storage.creditsPerGBDay)} credits (
            {fmtRateUSD(storage.usdPerGBDay)}) per GB-day
            {storage.freeGB > 0 ? `, first ${fmtGB(storage.freeGB)} free` : ""}.
          </p>
        ) : null}
      </div>
    </Panel>
  );
}

function UsagePanel({ billing }: { billing: Billing }) {
  return (
    <Panel title="Recent usage">
      {billing.usage.length === 0 ? (
        <div className="p-4 text-xs text-[var(--muted)]">
          No runs have been charged yet.
        </div>
      ) : (
        <table className="w-full text-sm">
          <thead className="border-b border-[var(--border)]">
            <tr>
              <th scope="col" className={thClass}>
                Run
              </th>
              <th scope="col" className={`${thClass} text-right`}>
                Seconds
              </th>
              <th scope="col" className={`${thClass} text-right`}>
                Credits
              </th>
              <th scope="col" className={`${thClass} text-right`}>
                When
              </th>
            </tr>
          </thead>
          <tbody>
            {billing.usage.map((u) => (
              <tr
                key={u.run_id}
                className="border-b border-[var(--border)] last:border-0"
              >
                <td className={`${tdClass} font-mono truncate max-w-[16rem]`}>
                  <Link
                    href={`/runs?run=${encodeURIComponent(u.run_id)}`}
                    className="text-[var(--accent)] hover:underline"
                  >
                    {u.run_id}
                  </Link>
                </td>
                <td className={`${tdClass} text-right font-mono`}>
                  {fmtCredits(u.seconds)}
                </td>
                <td className={`${tdClass} text-right font-mono`}>
                  {fmtCredits(amountCredits(u.amount_micro, billing))}
                </td>
                <td className={`${tdClass} text-right text-[var(--muted)]`}>
                  {fmtDateTime(unixSecondsISO(u.last_charged_at))}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {billing.storage_charged_micro > 0 ? (
        <div className="px-4 py-3 border-t border-[var(--border)] text-xs text-[var(--muted)]">
          Storage charged:{" "}
          {fmtCredits(amountCredits(billing.storage_charged_micro, billing))}{" "}
          credits.
        </div>
      ) : null}
    </Panel>
  );
}

function HistoryPanel({ billing }: { billing: Billing }) {
  return (
    <Panel title="History">
      {billing.grants.length === 0 ? (
        <div className="p-4 text-xs text-[var(--muted)]">No history yet.</div>
      ) : (
        <table className="w-full text-sm">
          <thead className="border-b border-[var(--border)]">
            <tr>
              <th scope="col" className={thClass}>
                Kind
              </th>
              <th scope="col" className={`${thClass} text-right`}>
                Credits
              </th>
              <th scope="col" className={`${thClass} text-right`}>
                Amount
              </th>
              <th scope="col" className={`${thClass} text-right`}>
                Date
              </th>
              <th scope="col" className={thClass}>
                Reference
              </th>
            </tr>
          </thead>
          <tbody>
            {billing.grants.map((g) => (
              <tr
                key={g.id}
                className="border-b border-[var(--border)] last:border-0"
              >
                <td className={tdClass}>{grantLabel(g.kind)}</td>
                <td className={`${tdClass} text-right font-mono`}>
                  {fmtCredits(amountCredits(g.amount_micro, billing))}
                </td>
                <td className={`${tdClass} text-right font-mono`}>
                  {fmtUSD(amountUSD(g.amount_micro, billing))}
                </td>
                <td className={`${tdClass} text-right text-[var(--muted)]`}>
                  {fmtDateTime(unixSecondsISO(g.created_at))}
                </td>
                <td
                  className={`${tdClass} font-mono text-xs text-[var(--muted)] truncate max-w-[12rem]`}
                >
                  {g.reference || "-"}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Panel>
  );
}

function CardPanel({ billing }: { billing: Billing }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if (!billing.card && !billing.can_add_card) return null;

  async function go() {
    setBusy(true);
    setError("");
    try {
      window.location.assign(await openCardPage(!!billing.card));
    } catch (err) {
      setError(errorText(err));
      setBusy(false);
    }
  }

  return (
    <Panel title="Card on file">
      <div className="p-4 space-y-2 text-sm">
        {billing.card ? (
          <p>
            A card was added on {fmtDateTime(unixSecondsISO(billing.card.added_at))}.
            Stripe holds it; Manage card shows and changes it.
          </p>
        ) : (
          <p className="text-[var(--muted)]">
            Save a card to turn on automatic refill.
          </p>
        )}
        {billing.can_add_card ? (
          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              className={buttonClass}
              disabled={busy}
              onClick={go}
            >
              {billing.card ? "Manage card" : "Add a card"}
            </button>
          </div>
        ) : null}
        {error ? (
          <p role="alert" className="text-red-300">
            {error}
          </p>
        ) : null}
      </div>
    </Panel>
  );
}

function centsText(cents: number): string {
  return cents > 0 ? String(cents / 100) : "";
}

function RefillPanel({
  billing,
  reload,
}: {
  billing: Billing;
  reload: () => Promise<void>;
}) {
  const r = billing.refill;
  const owner = billing.can_add_card;
  const [enabled, setEnabled] = useState(r.enabled);
  const [threshold, setThreshold] = useState(() =>
    centsText(r.threshold_cents || suggestedRefill.threshold_cents),
  );
  const [amount, setAmount] = useState(() =>
    centsText(r.amount_cents || suggestedRefill.amount_cents),
  );
  const [week, setWeek] = useState(() => centsText(r.week_limit_cents));
  const [month, setMonth] = useState(() => centsText(r.month_limit_cents));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);

  // An empty limit field means no limit, which the controller reads as zero.
  const limit = (text: string) => (text.trim() === "" ? 0 : parseDollars(text));
  const form = {
    enabled,
    threshold: parseDollars(threshold),
    amount: parseDollars(amount),
    week: limit(week),
    month: limit(month),
  };
  const problem = refillProblem(form, r);
  const status = refillStatus(r);
  const needsCard = enabled && !billing.card;

  async function save(e: React.FormEvent) {
    e.preventDefault();
    if (!owner || problem || needsCard) return;
    setBusy(true);
    setError("");
    setSaved(false);
    try {
      await setRefill({
        enabled,
        threshold_cents: form.threshold ?? 0,
        amount_cents: form.amount ?? 0,
        week_limit_cents: form.week ?? 0,
        month_limit_cents: form.month ?? 0,
      });
      setSaved(true);
      await reload();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  const field = (
    id: string,
    label: string,
    value: string,
    set: (v: string) => void,
    placeholder?: string,
  ) => (
    <div>
      <label htmlFor={id} className={labelClass}>
        {label}
      </label>
      <div className="flex items-center gap-2">
        <span className="text-sm text-[var(--muted)]" aria-hidden="true">
          $
        </span>
        <input
          id={id}
          inputMode="decimal"
          autoComplete="off"
          className={`${inputClass} w-28`}
          value={value}
          placeholder={placeholder}
          onChange={(e) => {
            set(e.target.value);
            setSaved(false);
            setError("");
          }}
        />
      </div>
    </div>
  );

  const caps = r.caps;
  const capped =
    caps.amount_cents > 0 || caps.per_day > 0 || caps.week_cents > 0;

  return (
    <Panel
      title="Automatic refill"
      hint="When the balance falls below the threshold, the saved card is charged the refill amount. Off unless you turn it on."
    >
      <form onSubmit={save} className="p-4 space-y-3 text-sm">
        {status ? (
          <p role="alert" className="text-red-300">
            {status}
          </p>
        ) : null}
        {r.open ? (
          <p role="status" className="text-[var(--muted)]">
            A refill is in progress.
          </p>
        ) : null}
        {owner ? null : (
          <p className="text-[var(--muted)]">
            Only a team owner can change automatic refill.
          </p>
        )}
        <fieldset disabled={!owner || busy} className="space-y-3">
          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={enabled}
              onChange={(e) => {
                setEnabled(e.target.checked);
                setSaved(false);
                setError("");
              }}
            />
            Refill automatically
          </label>
          <div className="flex flex-wrap gap-4">
            {field(
              "refill-threshold",
              "When the balance is below",
              threshold,
              setThreshold,
            )}
            {field("refill-amount", "Add this amount", amount, setAmount)}
            {field("refill-week", "Weekly limit", week, setWeek, "No limit")}
            {field(
              "refill-month",
              "Monthly limit",
              month,
              setMonth,
              "No limit",
            )}
          </div>
          <button
            type="submit"
            className={buttonClass}
            disabled={problem !== null || needsCard}
          >
            {busy ? "Saving…" : "Save"}
          </button>
        </fieldset>
        <p
          className={`text-xs ${(problem || needsCard) && owner ? "text-red-300" : "text-[var(--muted)]"}`}
        >
          {problem && owner
            ? problem
            : needsCard && owner
              ? "Save a card before turning on automatic refill."
              : `A refill is between ${fmtCents(r.min_amount_cents)} and ${fmtCents(r.max_amount_cents)}. Leave a limit empty for none.`}
        </p>
        <p className="text-[var(--muted)]">
          Refills in the last 7 days: {fmtCents(r.week_cents)}. Last 30 days:{" "}
          {fmtCents(r.month_cents)}.
        </p>
        <p className="text-[var(--muted)]">
          {capped
            ? `Your payment history allows at most ${fmtCents(caps.amount_cents)} per refill, ${caps.per_day} refill${caps.per_day === 1 ? "" : "s"} a day and ${fmtCents(caps.week_cents)} a week.`
            : `Each payment is at most ${fmtCents(r.max_amount_cents)}. Your team's weekly and monthly limits also apply.`}{" "}
          A refill a limit stops is not made, and Cloud work stops when the
          credits run out.
        </p>
        {saved ? (
          <p role="status" className="text-green-300">
            Saved.
          </p>
        ) : null}
        {error ? (
          <p role="alert" className="text-red-300">
            {error}
          </p>
        ) : null}
      </form>
    </Panel>
  );
}
function StandingPanel({
  billing,
  reload,
}: {
  billing: Billing;
  reload: () => Promise<void>;
}) {
  const s = billing.standing;
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const note = prepayNote(s);

  async function send(e: React.FormEvent) {
    e.preventDefault();
    const problem = standingRequestProblem(message);
    if (problem) {
      setError(problem);
      return;
    }
    setBusy(true);
    try {
      await requestHighStanding(message);
      setMessage("");
      setError("");
      await reload();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Panel
      title="Standing"
      hint="Payment history and prepaid funds set standing, which limits Cloud concurrency and job length. Payment history alone sets refill caps. Contact us for higher tiers, higher limits or larger payments."
    >
      <div className="p-4 space-y-2 text-sm">
        <p>
          {s.tier} standing. {note}
        </p>
        <p>
          {billing.limits.cloud_vcpu} concurrent Cloud vCPU and{" "}
          {billing.limits.max_job_seconds / 3600} hours per job.
        </p>
        {s.request ? (
          <p role="status" className="text-[var(--muted)]">
            {standingOutcome(s.request)}
          </p>
        ) : null}
        {s.can_request ? (
          <form onSubmit={send} className="space-y-2">
            <label htmlFor="standing-message" className={labelClass}>
              Contact us for higher tiers, higher limits or larger payments
            </label>
            <textarea
              id="standing-message"
              className={`${inputClass} w-full`}
              rows={3}
              placeholder="What higher limits or larger payments does the team need?"
              value={message}
              onChange={(e) => setMessage(e.target.value)}
            />
            <button type="submit" className={buttonClass} disabled={busy}>
              Send request
            </button>
          </form>
        ) : null}
        {error ? (
          <p role="alert" className="text-red-300">
            {error}
          </p>
        ) : null}
      </div>
    </Panel>
  );
}
