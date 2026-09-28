"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { type ReactNode, Suspense, useCallback, useEffect, useState } from "react";
import {
  Notice,
  Panel,
  buttonClass,
  errorText,
  inputClass,
  quietButtonClass,
} from "@/components/TeamShell";
import { toast } from "@/components/Toasts";
import { fmtCents } from "@/lib/billing";
import {
  type ActionKind,
  type OperatorTeam,
  type TeamMatch,
  actionEffect,
  actionLabels,
  actionProblem,
  actionRequest,
  balanceCents,
  getOperatorTeam,
  needsAmount,
  performAction,
  searchTeams,
} from "@/lib/operator";
import { unixSecondsISO } from "@/lib/teams";
import { fmtDateTime } from "@/lib/timeFormat";

export default function OperatorPage() {
  return (
    <Suspense>
      <OperatorConsole />
    </Suspense>
  );
}

const labelClass =
  "block text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1";

function OperatorConsole() {
  const team = useSearchParams().get("team") ?? "";
  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-4xl mx-auto w-full">
      <h1 className="text-xl font-bold mb-6">Operator console</h1>
      <TeamSearch />
      {team ? <TeamView key={team} slug={team} /> : null}
    </div>
  );
}

function TeamSearch() {
  const [q, setQ] = useState("");
  const [matches, setMatches] = useState<TeamMatch[] | null>(null);
  const [error, setError] = useState("");
  return (
    <Panel title="Find a team" hint="By slug, display name or owner email.">
      <form
        className="p-4 flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          setError("");
          searchTeams(q).then(setMatches, (err) => setError(errorText(err)));
        }}
      >
        <input
          aria-label="Search teams"
          className={`${inputClass} flex-1`}
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        <button className={buttonClass} type="submit">
          Search
        </button>
      </form>
      {error ? (
        <p role="alert" className="px-4 pb-4 text-sm text-red-400">
          {error}
        </p>
      ) : null}
      {matches?.length === 0 ? (
        <p className="px-4 pb-4 text-sm text-[var(--muted)]">
          No team matches.
        </p>
      ) : null}
      {matches?.length ? (
        <ul className="border-t border-[var(--border)] text-sm">
          {matches.map((m) => (
            <li key={m.team} className="px-4 py-2 flex gap-3">
              <Link
                className="underline"
                href={`/operator?team=${encodeURIComponent(m.team)}`}
              >
                {m.display_name || m.team}
              </Link>
              <span className="text-[var(--muted)]">{m.team}</span>
              <span className="text-[var(--muted)]">{m.owners.join(", ")}</span>
            </li>
          ))}
        </ul>
      ) : null}
    </Panel>
  );
}

function TeamView({ slug }: { slug: string }) {
  const [team, setTeam] = useState<OperatorTeam | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(() => {
    getOperatorTeam(slug).then(setTeam, (err) => setError(errorText(err)));
  }, [slug]);
  useEffect(load, [load]);
  if (error) return <Notice>{error}</Notice>;
  if (!team) return <Notice>Loading…</Notice>;
  const b = team.billing;
  return (
    <>
      <Panel
        title={`${team.display_name || team.team} (${team.team})`}
        hint={`Owners: ${team.owners.join(", ") || "none"}`}
      >
        <dl className="p-4 grid grid-cols-2 gap-3 text-sm">
          <Fact label="Balance">
            {fmtCents(balanceCents(team.balance_micro))}
          </Fact>
          <Fact label="Bought in 30 days">
            {fmtCents(b.purchased_30d_cents)} of{" "}
            {fmtCents(b.purchase_limit_cents)}
          </Fact>
          <Fact label="Trust">
            {b.trust} ({b.trusted ? "trusted" : "not trusted"})
            {b.trust_by ? (
              <span className="block text-xs text-[var(--muted)]">
                by {b.trust_by}
                {b.trust_at
                  ? ` at ${fmtDateTime(unixSecondsISO(b.trust_at))}`
                  : ""}
                : {b.trust_reason}
              </span>
            ) : null}
          </Fact>
          <Fact label="Limit override">
            {b.limit_override_cents ? fmtCents(b.limit_override_cents) : "none"}
          </Fact>
          <Fact label="Frozen">
            {team.frozen ? `yes: ${team.holds.join(", ")}` : "no"}
          </Fact>
        </dl>
      </Panel>
      <ActionForm team={team} onDone={load} />
      <Panel title="Recent business events">
        {team.events.length === 0 ? (
          <p className="p-4 text-sm text-[var(--muted)]">No events recorded.</p>
        ) : (
          <ul className="text-sm">
            {team.events.map((ev, i) => (
              <li
                key={i}
                className="px-4 py-2 border-b border-[var(--border)] last:border-0"
              >
                <span className="text-[var(--muted)]">
                  {fmtDateTime(unixSecondsISO(ev.at))}
                </span>{" "}
                <span className="font-mono">{ev.kind}</span>
                {ev.actor ? ` by ${ev.actor}` : ""}
                {ev.attrs?.reason ? `: ${String(ev.attrs.reason)}` : ""}
              </li>
            ))}
          </ul>
        )}
      </Panel>
    </>
  );
}

function Fact({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <div>
      <dt className={labelClass}>{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

function availableActions(team: OperatorTeam): ActionKind[] {
  const b = team.billing;
  const out: ActionKind[] = [];
  if (b.trust !== "granted") out.push("grant-trust");
  if (b.trust !== "revoked") out.push("revoke-trust");
  if (b.trust !== "automatic") out.push("reset-trust");
  out.push("set-limit");
  if (b.limit_override_cents) out.push("clear-limit");
  out.push("grant-credits", team.frozen ? "unfreeze" : "freeze");
  return out;
}

function ActionForm({
  team,
  onDone,
}: {
  team: OperatorTeam;
  onDone: () => void;
}) {
  const actions = availableActions(team);
  const [kind, setKind] = useState<ActionKind>(actions[0]);
  const [amount, setAmount] = useState("");
  const [reason, setReason] = useState("");
  // key is minted per review, so a retried confirmation cannot grant twice.
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const current = actions.includes(kind) ? kind : actions[0];
  const problem = actionProblem(current, reason, amount);

  const confirm = async () => {
    setBusy(true);
    setError("");
    try {
      await performAction(
        actionRequest(team.team, current, reason, amount, key),
      );
      toast(`${actionLabels[current]}: done for ${team.team}.`);
      setKey("");
      setReason("");
      setAmount("");
      onDone();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Panel
      title="Act on this team"
      hint="Every action is recorded with you as its actor and your reason."
    >
      <div className="p-4 space-y-3 text-sm">
        <div className="flex gap-3">
          <label className="flex-1">
            <span className={labelClass}>Action</span>
            <select
              className={`${inputClass} w-full`}
              value={current}
              disabled={key !== ""}
              onChange={(e) => setKind(e.target.value as ActionKind)}
            >
              {actions.map((a) => (
                <option key={a} value={a}>
                  {actionLabels[a]}
                </option>
              ))}
            </select>
          </label>
          {needsAmount(current) ? (
            <label className="flex-1">
              <span className={labelClass}>Amount in US dollars</span>
              <input
                className={`${inputClass} w-full`}
                value={amount}
                disabled={key !== ""}
                onChange={(e) => setAmount(e.target.value)}
              />
            </label>
          ) : null}
        </div>
        <label className="block">
          <span className={labelClass}>Reason</span>
          <textarea
            className={`${inputClass} w-full`}
            rows={2}
            value={reason}
            disabled={key !== ""}
            onChange={(e) => setReason(e.target.value)}
          />
        </label>
        {key === "" ? (
          <div className="flex items-center gap-3">
            <button
              className={buttonClass}
              disabled={problem !== ""}
              onClick={() => setKey(crypto.randomUUID())}
            >
              Review
            </button>
            {problem ? (
              <span className="text-[var(--muted)]">{problem}</span>
            ) : null}
          </div>
        ) : (
          <div
            role="alertdialog"
            aria-label="Confirm action"
            className="border border-amber-500/50 rounded-lg p-3 space-y-2"
          >
            <p className="font-bold">
              {actionLabels[current]} on {team.display_name || team.team} (
              {team.team})
            </p>
            <p>{actionEffect(team, current, amount)}</p>
            <p className="text-[var(--muted)]">Reason: {reason.trim()}</p>
            <div className="flex gap-2">
              <button className={buttonClass} disabled={busy} onClick={confirm}>
                Confirm
              </button>
              <button
                className={quietButtonClass}
                disabled={busy}
                onClick={() => setKey("")}
              >
                Cancel
              </button>
            </div>
          </div>
        )}
        {error ? (
          <p role="alert" className="text-red-400">
            {error}
          </p>
        ) : null}
      </div>
    </Panel>
  );
}
