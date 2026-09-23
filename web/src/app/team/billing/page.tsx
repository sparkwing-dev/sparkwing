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
  fmtUSDShort,
  getBilling,
  grantLabel,
  parseDollars,
  priceRows,
  purchaseProblem,
  startCheckout,
  storagePrice,
  vcpuHourPrice,
} from "@/lib/billing";
import { billingEnabled, unixSecondsISO } from "@/lib/teams";
import { fmtDateTime } from "@/lib/timeFormat";

export default function BillingPage() {
  return (
    <Suspense>
      <TeamShell>{(_, caps) => billingEnabled(caps) ? <BillingRoute /> : <Notice>Billing is not enabled on this controller.</Notice>}</TeamShell>
    </Suspense>
  );
}

const labelClass =
  "block text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1";
const thClass =
  "px-4 py-2 text-left text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]";
const tdClass = "px-4 py-2";

function BillingRoute() {
  const returned = checkoutReturn(useSearchParams().get("checkout"));
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
          {billing.frozen ? (
            <div
              role="alert"
              className="mb-6 rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-300"
            >
              Cloud runs for this team are paused while a payment dispute is
              open. Contact support to resolve it.
            </div>
          ) : null}
          <BalancePanel billing={billing} />
          {billing.checkout_enabled ? <BuyPanel billing={billing} /> : null}
          <PricesPanel billing={billing} />
          <UsagePanel billing={billing} />
          <GrantsPanel billing={billing} />
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
          {fmtUSD(balanceUSD(billing.balance_micro, billing))} · a team balance
          holds up to{" "}
          {fmtUSDShort(balanceUSD(billing.balance_cap_micro, billing))}
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
      window.location.assign(await startCheckout(cents, billing));
    } catch (err) {
      setSubmitError(errorText(err));
      setBusy(false);
    }
  }

  return (
    <Panel
      title="Buy credits"
      hint={`Pay with Stripe, from ${fmtCents(billing.purchase_min_cents)} to ${fmtCents(billing.purchase_max_cents)} at a time.`}
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
              {busy ? "Opening checkout…" : "Buy credits"}
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
          Purchases are final. Credits never expire. 1 credit = 1 vCPU-second.
        </p>
      </form>
    </Panel>
  );
}

function PricesPanel({ billing }: { billing: Billing }) {
  const rows = priceRows(billing);
  const headline = vcpuHourPrice(billing);
  const storage = storagePrice(billing);
  return (
    <Panel
      title="Prices"
      hint={
        headline
          ? `${headline.uniform ? "" : "From "}${fmtRateUSD(headline.usd)} per vCPU-hour, billed by the second.`
          : undefined
      }
    >
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
                Credits / minute
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
                  {fmtRate(r.creditsPerMinute)}
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
          Each node is billed for at least {billing.min_billable_seconds}{" "}
          seconds. {fmtCredits(billing.credits_per_dollar)} credits cost $1.
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
    <Panel
      title="Recent usage"
      hint="Runner time charged per run, newest first."
    >
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

function GrantsPanel({ billing }: { billing: Billing }) {
  return (
    <Panel
      title="Purchases"
      hint="Purchases, grants and refunds, newest first."
    >
      {billing.grants.length === 0 ? (
        <div className="p-4 text-xs text-[var(--muted)]">No purchases yet.</div>
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
