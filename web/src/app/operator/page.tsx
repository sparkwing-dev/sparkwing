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
  type ActionExtra,
  type ActionKind,
  type OperatorStandingRequest,
  type OperatorTeam,
  type TeamMatch,
  type WaitlistPage,
  actionEffect,
  actionLabels,
  actionProblem,
  actionRequest,
  approveStandingRequest,
  approveWaitlisted,
  availableActions,
  balanceCents,
  declineStandingRequest,
  disputeHolds,
  getOperatorTeam,
  getStandingRequests,
  getWaitlist,
  historyFacts,
  needsAmount,
  performAction,
  searchTeams,
  waitlistPageSize,
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
  const params = useSearchParams();
  const team = params.get("team") ?? "";
  const view = params.get("view");
  const waitlist = view === "waitlist";
  const standing = view === "standing";
  const [waiting, setWaiting] = useState<number | null>(null);
  useEffect(() => {
    getWaitlist(0, 1).then(
      (p) => setWaiting(p.total),
      () => setWaiting(null),
    );
  }, []);
  const tab = (active: boolean) =>
    `px-3 py-1.5 rounded-[var(--radius-control)] ${active ? "bg-[var(--surface-raised)] font-bold" : "text-[var(--muted)] hover:text-[var(--foreground)]"}`;
  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-4xl mx-auto w-full">
      <h1 className="text-xl font-bold mb-4">Operator console</h1>
      <nav aria-label="Operator console" className="flex gap-2 mb-6 text-sm">
        <Link className={tab(!waitlist && !standing)} href="/operator">
          Teams
        </Link>
        <Link className={tab(standing)} href="/operator?view=standing">
          Standing requests
        </Link>
        <Link className={tab(waitlist)} href="/operator?view=waitlist">
          Waitlist{waiting === null ? "" : ` (${waiting})`}
        </Link>
      </nav>
      {waitlist ? (
        <WaitlistView onTotal={setWaiting} />
      ) : standing ? (
        <StandingQueue />
      ) : (
        <>
          <TeamSearch />
          {team ? <TeamView key={team} slug={team} /> : null}
        </>
      )}
    </div>
  );
}

function StandingQueue() {
  const [items, setItems] = useState<OperatorStandingRequest[] | null>(null);
  const [reasons, setReasons] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const load = useCallback(() => {
    getStandingRequests().then(setItems, (err) => setError(errorText(err)));
  }, []);
  useEffect(load, [load]);

  const decide = async (item: OperatorStandingRequest, approve: boolean) => {
    const team = item.request.team;
    const reason = (reasons[item.request.id] ?? "").trim();
    if (reason === "") {
      setError("Give a reason; the requester sees it and it is recorded.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      if (approve) await approveStandingRequest(team, reason);
      else await declineStandingRequest(team, reason);
      toast(approve ? `Granted ${team} High standing.` : `Declined ${team}'s request.`);
      load();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };

  if (!items) return error ? <Notice>{error}</Notice> : <Notice>Loading…</Notice>;
  return (
    <Panel
      title="Standing requests"
      hint="Oldest first. Approving grants the team High standing; declining sends the reason to its owners."
    >
      {error ? (
        <p role="alert" className="px-4 pt-3 text-sm text-red-400">
          {error}
        </p>
      ) : null}
      {items.length === 0 ? (
        <p className="p-4 text-sm text-[var(--muted)]">No open requests.</p>
      ) : (
        <ul>
          {items.map((item) => (
            <li key={item.request.id} className="border-t border-[var(--border)] first:border-t-0 p-4 space-y-2 text-sm">
              <div className="flex flex-wrap items-baseline gap-2">
                <Link className="font-bold" href={`/operator?team=${encodeURIComponent(item.request.team)}`}>
                  {item.display_name || item.request.team}
                </Link>
                <span className="text-[var(--muted)]">
                  {item.request.requested_by || "former member"} asked for {item.request.tier} on{" "}
                  {fmtDateTime(unixSecondsISO(item.request.requested_at))}
                </span>
              </div>
              <p>
                {item.standing} standing, prepaid funds reach {item.prepay_ceiling}. {historyFacts(item.history)}.
                Balance {fmtCents(balanceCents(item.balance_micro))}.
              </p>
              <blockquote className="border-l-2 border-[var(--border)] pl-3 whitespace-pre-wrap">
                {item.request.message}
              </blockquote>
              <div className="flex flex-wrap items-center gap-2">
                <label className="sr-only" htmlFor={`reason-${item.request.id}`}>
                  Reason for {item.request.team}
                </label>
                <input
                  id={`reason-${item.request.id}`}
                  className={`${inputClass} flex-1 min-w-48`}
                  maxLength={500}
                  placeholder="Reason"
                  value={reasons[item.request.id] ?? ""}
                  onChange={(e) => setReasons((cur) => ({ ...cur, [item.request.id]: e.target.value }))}
                />
                <button className={buttonClass} disabled={busy} onClick={() => decide(item, true)}>
                  Grant High standing
                </button>
                <button className={quietButtonClass} disabled={busy} onClick={() => decide(item, false)}>
                  Decline
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  );
}

function WaitlistView({ onTotal }: { onTotal: (n: number) => void }) {
  const [offset, setOffset] = useState(0);
  const [page, setPage] = useState<WaitlistPage | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const load = useCallback(() => {
    getWaitlist(offset).then(
      (p) => {
        // Approving the last rows of a later page leaves it empty.
        if (p.accounts.length === 0 && offset > 0) {
          setOffset(Math.max(offset - waitlistPageSize, 0));
          return;
        }
        setPage(p);
        onTotal(p.total);
      },
      (err) => setError(errorText(err)),
    );
  }, [offset, onTotal]);
  useEffect(load, [load]);

  const approve = async (ids: string[]) => {
    setBusy(true);
    setError("");
    try {
      const emails = await approveWaitlisted(ids);
      toast(
        emails.length === 1
          ? `Approved ${emails[0]}.`
          : `Approved ${emails.length} accounts.`,
      );
      setSelected(new Set());
      load();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };

  if (!page) return error ? <Notice>{error}</Notice> : <Notice>Loading…</Notice>;
  const rows = page.accounts;
  const allOnPage = rows.length > 0 && rows.every((a) => selected.has(a.id));
  const toggle = (id: string) =>
    setSelected((cur) => {
      const next = new Set(cur);
      if (!next.delete(id)) next.add(id);
      return next;
    });
  return (
    <Panel
      title="Waitlist"
      hint="Newest first. Approving gives each person their own space and sends the approval notice."
      action={
        <button
          className={buttonClass}
          disabled={busy || selected.size === 0}
          onClick={() => approve([...selected])}
        >
          Approve selected ({selected.size})
        </button>
      }
    >
      {error ? (
        <p role="alert" className="px-4 pt-3 text-sm text-red-400">
          {error}
        </p>
      ) : null}
      {rows.length === 0 ? (
        <p className="p-4 text-sm text-[var(--muted)]">Nobody is waiting.</p>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] text-left">
              <th className="px-4 py-2 w-8">
                <input
                  type="checkbox"
                  aria-label="Select every account on this page"
                  checked={allOnPage}
                  onChange={() =>
                    setSelected((cur) => {
                      const next = new Set(cur);
                      for (const a of rows) {
                        if (allOnPage) next.delete(a.id);
                        else next.add(a.id);
                      }
                      return next;
                    })
                  }
                />
              </th>
              <th className="px-2 py-2">Email</th>
              <th className="px-2 py-2">Name</th>
              <th className="px-2 py-2">Provider</th>
              <th className="px-2 py-2">Waitlisted</th>
              <th className="px-4 py-2" />
            </tr>
          </thead>
          <tbody>
            {rows.map((a) => (
              <tr key={a.id} className="border-t border-[var(--border)]">
                <td className="px-4 py-2">
                  <input
                    type="checkbox"
                    aria-label={`Select ${a.email}`}
                    checked={selected.has(a.id)}
                    onChange={() => toggle(a.id)}
                  />
                </td>
                <td className="px-2 py-2">{a.email}</td>
                <td className="px-2 py-2">{a.name}</td>
                <td className="px-2 py-2">{a.provider || "unknown"}</td>
                <td className="px-2 py-2 text-[var(--muted)] whitespace-nowrap">
                  {fmtDateTime(unixSecondsISO(a.waitlisted_at))}
                </td>
                <td className="px-4 py-2 text-right">
                  <button
                    className={quietButtonClass}
                    disabled={busy}
                    aria-label={`Approve ${a.email}`}
                    onClick={() => approve([a.id])}
                  >
                    Approve
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {page.total > waitlistPageSize ? (
        <div className="px-4 py-3 flex items-center gap-3 border-t border-[var(--border)] text-sm">
          <button
            className={quietButtonClass}
            disabled={offset === 0}
            onClick={() => setOffset(Math.max(offset - waitlistPageSize, 0))}
          >
            Newer
          </button>
          <span className="text-[var(--muted)]">
            {offset + 1}–{offset + rows.length} of {page.total}
          </span>
          <button
            className={quietButtonClass}
            disabled={offset + rows.length >= page.total}
            onClick={() => setOffset(offset + waitlistPageSize)}
          >
            Older
          </button>
        </div>
      ) : null}
    </Panel>
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
          <Fact label="Standing">{b.standing}: {b.cloud_vcpu} Cloud vCPU, {b.max_job_seconds / 3600} hours per job</Fact>
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
  const [extra, setExtra] = useState<ActionExtra>({});
  const [reason, setReason] = useState("");
  // key is minted per review, so a retried confirmation cannot grant twice.
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const current = actions.includes(kind) ? kind : actions[0];
  const problem = actionProblem(current, reason, amount, extra);
  const setField = (field: keyof ActionExtra) => (value: string) =>
    setExtra((cur) => ({ ...cur, [field]: value }));

  const confirm = async () => {
    setBusy(true);
    setError("");
    try {
      await performAction(
        actionRequest(team.team, current, reason, amount, key, extra),
      );
      toast(`${actionLabels[current]}: done for ${team.team}.`);
      setKey("");
      setReason("");
      setAmount("");
      setExtra({});
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
          {current === "release-hold" ? (
            <label className="flex-1">
              <span className={labelClass}>Hold</span>
              <select
                aria-label="Dispute hold to release"
                className={`${inputClass} w-full`}
                value={extra.hold ?? ""}
                disabled={key !== ""}
                onChange={(e) => setField("hold")(e.target.value)}
              >
                <option value="">Choose a hold</option>
                {disputeHolds(team).map((h) => (
                  <option key={h} value={h}>
                    {h}
                  </option>
                ))}
              </select>
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
            <p>{actionEffect(team, current, amount, extra)}</p>
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
