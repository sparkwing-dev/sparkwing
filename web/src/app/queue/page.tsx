"use client";

// Queue: the one truthful view of local admission, mirroring
// `sparkwing queue`. Every resource with its headroom arithmetic, every
// holder with elapsed time and cost, every waiter in arrival order with
// what it is waiting on and its ETA. The queue is the primary answer to
// "why isn't my run starting," so this is where a human looks first when
// the box feels busy. With no daemon running there is nothing to
// arbitrate, so the panel reports a calm empty state.

import { useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";
import {
  type QueueHolder,
  type QueueState,
  type QueueWaiter,
  getQueue,
} from "@/lib/api";
import {
  type HolderGroup,
  daemonUptimeLabel,
  driftNotes,
  eventsLine,
  externalPressureNote,
  fmtAmount,
  fmtCost,
  fmtDuration,
  fmtETA,
  fmtHolderCost,
  groupHolders,
  hasDaemon,
  resourceAvailable,
} from "@/lib/queue";
import Tooltip from "@/components/Tooltip";

const POLL_MS = 3000;

export default function QueuePage() {
  const [qs, setQs] = useState<QueueState | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [pulse, setPulse] = useState(false);
  const pulseTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const refresh = useCallback(async () => {
    const next = await getQueue();
    setQs(next);
    setLoaded(true);
    setPulse(true);
    if (pulseTimer.current) clearTimeout(pulseTimer.current);
    pulseTimer.current = setTimeout(() => setPulse(false), 600);
  }, []);

  useEffect(() => {
    refresh();
    const i = window.setInterval(() => {
      if (!document.hidden) refresh();
    }, POLL_MS);
    return () => {
      window.clearInterval(i);
      if (pulseTimer.current) clearTimeout(pulseTimer.current);
    };
  }, [refresh]);

  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-5xl mx-auto w-full">
      <Header qs={qs} pulse={pulse} />
      {!loaded ? (
        <Skeleton />
      ) : !qs || !hasDaemon(qs) ? (
        <EmptyState />
      ) : (
        <QueueBody qs={qs} />
      )}
    </div>
  );
}

function Header({ qs, pulse }: { qs: QueueState | null; pulse: boolean }) {
  const holders = qs?.holders?.length ?? 0;
  const waiters = qs?.waiters?.length ?? 0;
  const running = qs != null && hasDaemon(qs);
  const version = qs?.daemon_version || "";
  const uptime = qs ? daemonUptimeLabel(qs) : "";
  const events = eventsLine(qs?.events);
  const clear = qs?.expected_clear_ms;
  return (
    <div className="mb-5">
      <div className="flex items-baseline justify-between mb-1">
        <div className="flex items-center gap-2">
          <h1 className="text-xl font-bold">Admission queue</h1>
          <Tooltip
            content={
              running
                ? "Live from the local admission daemon"
                : "No admission daemon running"
            }
          >
            <span
              className={`inline-block w-2 h-2 rounded-full cursor-default ${
                running
                  ? `bg-[var(--success)] ${pulse ? "animate-ping-once" : ""}`
                  : "bg-[var(--muted)]"
              }`}
            />
          </Tooltip>
        </div>
        <span className="text-[10px] font-mono text-[var(--muted)]">
          refresh every {POLL_MS / 1000}s
        </span>
      </div>
      {running ? (
        <>
          <div className="text-sm text-[var(--muted)]">
            {holders} holding, {waiters} queued
            {clear != null && clear > 0 ? (
              <> · clears in ~{fmtDuration(clear)}</>
            ) : null}
          </div>
          {(version || uptime) && (
            <div className="text-[11px] font-mono text-[var(--muted)] mt-0.5">
              daemon {version || "unknown"}
              {uptime ? `, ${uptime}` : ""}
            </div>
          )}
          {events && (
            <div className="text-[11px] font-mono text-[var(--muted)] mt-0.5">
              {events}
            </div>
          )}
        </>
      ) : null}
    </div>
  );
}

function QueueBody({ qs }: { qs: QueueState }) {
  const groups = groupHolders(qs.holders ?? []);
  const waiters = qs.waiters ?? [];
  const pressure = externalPressureNote(qs);
  const drifts = driftNotes(qs);
  return (
    <div className="flex flex-col gap-6">
      <ResourcesSection qs={qs} pressure={pressure} />
      <HoldersSection groups={groups} />
      <WaitersSection waiters={waiters} />
      {drifts.length > 0 && (
        <div className="flex flex-col gap-2">
          {drifts.map((d) => (
            <Callout key={d.runID} tone="warning">
              <span className="font-mono text-violet-300">{d.runID}</span>:{" "}
              {d.warning}
            </Callout>
          ))}
        </div>
      )}
    </div>
  );
}

function Section({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <h2 className="text-xs font-bold uppercase tracking-wider text-[var(--muted)] mb-2">
        {title}
      </h2>
      {children}
    </div>
  );
}

function ResourcesSection({
  qs,
  pressure,
}: {
  qs: QueueState;
  pressure: string;
}) {
  const rows = qs.resources ?? [];
  return (
    <Section title="Resources">
      <div className="overflow-x-auto rounded-lg border border-[var(--border)]">
        <table className="w-full text-sm">
          <thead>
            <tr className="text-[10px] uppercase tracking-wider text-[var(--muted)] bg-[var(--surface)]">
              <Th>Resource</Th>
              <Th right>Capacity</Th>
              <Th right>In use</Th>
              <Th right hideSm>
                Reserved
              </Th>
              <Th right hideSm>
                External
              </Th>
              <Th right>Available</Th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 ? (
              <tr>
                <td
                  colSpan={6}
                  className="px-3 py-3 text-[var(--muted)] text-center"
                >
                  No resource dimensions reported.
                </td>
              </tr>
            ) : (
              rows.map((r) => {
                const host = r.key === "cores" || r.key === "memory";
                return (
                  <tr
                    key={r.key}
                    className="border-t border-[var(--border)] bg-[var(--surface)]"
                  >
                    <Td>
                      <span className="font-mono text-[var(--foreground)]">
                        {r.key}
                      </span>
                    </Td>
                    <Td right mono>
                      {fmtAmount(r.key, r.capacity)}
                    </Td>
                    <Td right mono>
                      {fmtAmount(r.key, r.held)}
                    </Td>
                    <Td right mono hideSm muted>
                      {host ? fmtAmount(r.key, r.reserved ?? 0) : "-"}
                    </Td>
                    <Td right mono hideSm muted>
                      {host ? fmtAmount(r.key, r.external ?? 0) : "-"}
                    </Td>
                    <Td right mono>
                      <span
                        className={
                          resourceAvailable(r) <= 0
                            ? "text-[var(--warning)]"
                            : "text-[var(--success)]"
                        }
                      >
                        {fmtAmount(r.key, resourceAvailable(r))}
                      </span>
                    </Td>
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>
      {pressure && (
        <div className="mt-2">
          <Callout tone="warning">{pressure}</Callout>
        </div>
      )}
    </Section>
  );
}

function HoldersSection({ groups }: { groups: HolderGroup[] }) {
  return (
    <Section title="Holders">
      <div className="overflow-x-auto rounded-lg border border-[var(--border)]">
        <table className="w-full text-sm">
          <thead>
            <tr className="text-[10px] uppercase tracking-wider text-[var(--muted)] bg-[var(--surface)]">
              <Th>Run</Th>
              <Th hideSm>Pipeline</Th>
              <Th hideSm>Repo</Th>
              <Th right>Elapsed</Th>
              <Th>Cost</Th>
              <Th hideSm>Semaphores</Th>
            </tr>
          </thead>
          <tbody>
            {groups.length === 0 ? (
              <tr>
                <td
                  colSpan={6}
                  className="px-3 py-3 text-[var(--muted)] text-center bg-[var(--surface)]"
                >
                  Nothing is holding admission.
                </td>
              </tr>
            ) : (
              groups.flatMap((g) => [
                <HolderRow key={g.holder.run_id} h={g.holder} />,
                ...g.children.map((c) => (
                  <HolderRow key={c.run_id} h={c} attached />
                )),
              ])
            )}
          </tbody>
        </table>
      </div>
      {groups.flatMap((g) =>
        [g.holder, ...g.children]
          .filter((h) => h.stalled && h.recovery)
          .map((h) => (
            <div key={`rec-${h.run_id}`} className="mt-2">
              <Callout tone="danger">
                <span className="font-mono text-violet-300">{h.run_id}</span> is
                stalled (idle while runs wait). Recover with:
                <code className="block mt-1 font-mono text-[var(--foreground)] bg-[var(--background)] rounded px-2 py-1 overflow-x-auto">
                  {h.recovery}
                </code>
              </Callout>
            </div>
          )),
      )}
    </Section>
  );
}

function HolderRow({ h, attached }: { h: QueueHolder; attached?: boolean }) {
  return (
    <tr className="border-t border-[var(--border)] bg-[var(--surface)]">
      <Td>
        <div className={attached ? "pl-4 flex items-center gap-1.5" : ""}>
          {attached && (
            <span className="text-[var(--muted)] text-xs" aria-hidden="true">
              ↳
            </span>
          )}
          <RunLink id={h.run_id} label={queueRunLabel(h)} />
          {attached && (
            <Tooltip content="Rides its parent's lease; draws no budget of its own">
              <span className="text-[10px] font-mono text-[var(--muted)] cursor-default">
                attached
              </span>
            </Tooltip>
          )}
          {h.stalled && (
            <Tooltip content="Alive but near-zero CPU while runs wait behind it -- a likely wedge">
              <span className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-red-500/15 text-red-400 cursor-default">
                stalled
              </span>
            </Tooltip>
          )}
          {!h.stalled && h.contended && (
            <Tooltip content="Running slower than its measured profile while the host is saturated">
              <span className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-amber-500/15 text-amber-300 cursor-default">
                contended
              </span>
            </Tooltip>
          )}
        </div>
      </Td>
      <Td hideSm mono muted>
        {h.pipeline || "-"}
      </Td>
      <Td hideSm mono muted>
        {h.repo || "-"}
      </Td>
      <Td right mono>
        {fmtDuration(h.elapsed_ms)}
      </Td>
      <Td mono>
        <CostCell
          cost={fmtHolderCost(h)}
          source={h.parent ? "" : h.cost_source}
        />
      </Td>
      <Td hideSm mono muted>
        {h.semaphores && h.semaphores.length > 0
          ? h.semaphores.join(", ")
          : "-"}
      </Td>
    </tr>
  );
}

function WaitersSection({ waiters }: { waiters: QueueWaiter[] }) {
  return (
    <Section title="Waiting">
      <div className="overflow-x-auto rounded-lg border border-[var(--border)]">
        <table className="w-full text-sm">
          <thead>
            <tr className="text-[10px] uppercase tracking-wider text-[var(--muted)] bg-[var(--surface)]">
              <Th right>#</Th>
              <Th>Run</Th>
              <Th hideSm>Pipeline</Th>
              <Th hideSm>Repo</Th>
              <Th>Cost</Th>
              <Th right>ETA</Th>
              <Th>Waiting on</Th>
              <Th right hideSm>
                Waited
              </Th>
            </tr>
          </thead>
          <tbody>
            {waiters.length === 0 ? (
              <tr>
                <td
                  colSpan={8}
                  className="px-3 py-3 text-[var(--muted)] text-center bg-[var(--surface)]"
                >
                  No one is queued.
                </td>
              </tr>
            ) : (
              waiters.map((w) => (
                <tr
                  key={w.run_id}
                  className="border-t border-[var(--border)] bg-[var(--surface)] align-top"
                >
                  <Td right mono muted>
                    {w.position}
                  </Td>
                  <Td>
                    <RunLink id={w.run_id} label={queueRunLabel(w)} />
                  </Td>
                  <Td hideSm mono muted>
                    {w.pipeline || "-"}
                  </Td>
                  <Td hideSm mono muted>
                    {w.repo || "-"}
                  </Td>
                  <Td mono>
                    <CostCell
                      cost={fmtCost(w.resources)}
                      source={w.cost_source}
                    />
                  </Td>
                  <Td right mono>
                    {fmtETA(w.expected_start_ms)}
                  </Td>
                  <Td>
                    <WaitingOn keys={w.waiting_on} reason={w.blocking_reason} />
                  </Td>
                  <Td right mono muted hideSm>
                    {fmtDuration(w.waiting_ms ?? 0)}
                  </Td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </Section>
  );
}

function WaitingOn({ keys, reason }: { keys?: string[]; reason?: string }) {
  const label =
    keys && keys.length > 0
      ? keys.join(", ")
      : reason
        ? "capacity"
        : "arrival order";
  const body = (
    <span className="font-mono text-xs text-[var(--warning)] cursor-default">
      {label}
    </span>
  );
  if (!reason) return body;
  return <Tooltip content={reason}>{body}</Tooltip>;
}

function CostCell({ cost, source }: { cost: string; source?: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span>{cost}</span>
      {source && <SourceChip source={source} />}
    </span>
  );
}

function SourceChip({ source }: { source: string }) {
  const hint: Record<string, string> = {
    pin: "Cost fixed by an explicit pin",
    measured: "Cost from the run's measured profile",
    default: "Cost is the fallback default (no pin, no measurement yet)",
  };
  return (
    <Tooltip content={hint[source] || `Cost source: ${source}`}>
      <span className="text-[10px] font-mono px-1 py-0.5 rounded bg-[var(--surface-raised)] text-[var(--muted)] cursor-default">
        {source}
      </span>
    </Tooltip>
  );
}

function RunLink({ id, label }: { id: string; label?: string }) {
  return (
    <Link
      href={`/runs?run=${encodeURIComponent(id)}`}
      className="font-mono text-xs text-violet-300 hover:underline"
    >
      {label || id}
    </Link>
  );
}

function queueRunLabel(row: { run_id: string; display_run_id?: string }) {
  return row.display_run_id || row.run_id;
}

function Callout({
  tone,
  children,
}: {
  tone: "warning" | "danger";
  children: React.ReactNode;
}) {
  const cls =
    tone === "danger"
      ? "border-red-500/40 bg-red-500/10 text-red-300"
      : "border-amber-500/40 bg-amber-500/10 text-amber-300";
  return (
    <div className={`text-sm rounded-lg border px-3 py-2 ${cls}`}>
      {children}
    </div>
  );
}

function Th({
  children,
  right,
  hideSm,
}: {
  children: React.ReactNode;
  right?: boolean;
  hideSm?: boolean;
}) {
  return (
    <th
      className={`px-3 py-2 font-bold ${right ? "text-right" : "text-left"} ${
        hideSm ? "hidden sm:table-cell" : ""
      }`}
    >
      {children}
    </th>
  );
}

function Td({
  children,
  right,
  mono,
  muted,
  hideSm,
}: {
  children: React.ReactNode;
  right?: boolean;
  mono?: boolean;
  muted?: boolean;
  hideSm?: boolean;
}) {
  return (
    <td
      className={`px-3 py-2 ${right ? "text-right" : "text-left"} ${
        mono ? "font-mono text-xs" : ""
      } ${muted ? "text-[var(--muted)]" : ""} ${
        hideSm ? "hidden sm:table-cell" : ""
      }`}
    >
      {children}
    </td>
  );
}

function Skeleton() {
  return (
    <div className="flex flex-col gap-4 animate-pulse">
      {[0, 1, 2].map((i) => (
        <div
          key={i}
          className="h-28 rounded-lg border border-[var(--border)] bg-[var(--surface)]"
        />
      ))}
    </div>
  );
}

function EmptyState() {
  return (
    <div className="rounded-lg border border-[var(--border)] bg-[var(--surface)] p-6 flex items-start gap-3">
      <span className="w-2.5 h-2.5 rounded-full bg-[var(--muted)] shrink-0 mt-1.5" />
      <div>
        <div className="text-sm text-[var(--foreground)]">
          No admission daemon running; nothing is queued.
        </div>
        <div className="text-xs text-[var(--muted)] mt-1">
          The daemon starts automatically with your next run and arbitrates
          local capacity from there.
        </div>
      </div>
    </div>
  );
}
