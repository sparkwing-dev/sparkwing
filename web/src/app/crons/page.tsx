"use client";

import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import {
  type CronDetail,
  type CronFire,
  type CronSchedule,
  type CronsOverview,
  getCron,
  getCrons,
  pauseCron,
  resumeCron,
  runCronNow,
} from "@/lib/api";
import {
  type CronTone,
  declaredVsEffective,
  fmtArgs,
  healthBanner,
  lockBadge,
  lockNote,
  outcomeLabel,
  outcomeTone,
  overrideMarker,
  partitionUndeclared,
  scheduleCounts,
  shortRef,
  stateTone,
} from "@/lib/crons";
import { fmtAgo, fmtDateTime, fmtFullDate, fmtUntil } from "@/lib/timeFormat";
import { useCurrentTime } from "@/lib/useCurrentTime";
import StatusLabel from "@/components/StatusLabel";
import Tooltip from "@/components/Tooltip";
import { toast } from "@/components/Toasts";

const POLL_MS = 5000;

export default function CronsPage() {
  return (
    <Suspense>
      <CronsRoute />
    </Suspense>
  );
}

function CronsRoute() {
  const searchParams = useSearchParams();
  const router = useRouter();
  const selected = searchParams.get("schedule");
  const [overview, setOverview] = useState<CronsOverview | null>(null);
  const [detail, setDetail] = useState<CronDetail | null>(null);
  const [detailMissing, setDetailMissing] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [showUndeclared, setShowUndeclared] = useState(false);
  const [busyID, setBusyID] = useState<string | null>(null);
  const now = useCurrentTime();

  const selectedRef = useRef<string | null>(selected);
  selectedRef.current = selected;

  const refreshDetail = useCallback(async (id: string | null) => {
    if (!id) {
      setDetail(null);
      setDetailMissing(false);
      return;
    }
    const next = await getCron(id);
    // A slow response for a row the reader has already left behind would
    // otherwise overwrite the pane they are looking at now.
    if (selectedRef.current !== id) return;
    setDetail(next);
    setDetailMissing(next === null);
  }, []);

  const refresh = useCallback(async () => {
    setOverview(await getCrons());
    setLoaded(true);
    await refreshDetail(selectedRef.current);
  }, [refreshDetail]);

  useEffect(() => {
    let cancelled = false;
    queueMicrotask(() => {
      if (!cancelled) void refresh();
    });
    const i = window.setInterval(() => {
      if (!document.hidden) void refresh();
    }, POLL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(i);
    };
  }, [refresh]);

  useEffect(() => {
    void refreshDetail(selected);
  }, [selected, refreshDetail]);

  const select = useCallback(
    (id: string | null) => {
      const params = new URLSearchParams(searchParams.toString());
      if (id) params.set("schedule", id);
      else params.delete("schedule");
      const qs = params.toString();
      router.replace(qs ? `/crons?${qs}` : "/crons", { scroll: false });
    },
    [router, searchParams],
  );

  const act = useCallback(
    async (s: CronSchedule, kind: "pause" | "resume" | "run") => {
      setBusyID(s.id);
      try {
        if (kind === "pause") {
          await pauseCron(s.id);
          toast(`Paused ${s.name}`, "success");
        } else if (kind === "resume") {
          await resumeCron(s.id);
          toast(`Resumed ${s.name}`, "success");
        } else {
          const started = await runCronNow(s.id);
          toast(`Started ${started.run_id}`, "success");
        }
      } catch (err) {
        toast(err instanceof Error ? err.message : String(err), "error");
      } finally {
        setBusyID(null);
      }
      await refresh();
    },
    [refresh],
  );

  const schedules = overview?.schedules ?? [];
  const { listed, undeclared } = partitionUndeclared(schedules);
  const rows = showUndeclared ? [...listed, ...undeclared] : listed;

  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-6xl mx-auto w-full">
      <Header overview={overview} />
      {!loaded ? (
        <Skeleton />
      ) : (
        <div className="flex flex-col gap-5">
          <HealthBannerCard overview={overview} />
          {schedules.length === 0 ? (
            <EmptyState />
          ) : (
            <div className="flex flex-col lg:flex-row gap-5 items-start">
              <div className="flex-1 min-w-0 w-full flex flex-col gap-2">
                <ScheduleTable
                  rows={rows}
                  now={now}
                  selected={selected}
                  busyID={busyID}
                  onSelect={select}
                  onAct={act}
                />
                {undeclared.length > 0 && (
                  <div>
                    <button
                      type="button"
                      onClick={() => setShowUndeclared((v) => !v)}
                      className="text-xs text-[var(--muted)] hover:text-[var(--foreground)] underline underline-offset-2"
                    >
                      {`${showUndeclared ? "hide" : "show"} undeclared (${undeclared.length})`}
                    </button>
                  </div>
                )}
              </div>
              {selected && (
                <div className="w-full lg:w-[26rem] lg:shrink-0">
                  <DetailPane
                    detail={detail}
                    missing={detailMissing}
                    now={now}
                    onClose={() => select(null)}
                  />
                </div>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function Header({ overview }: { overview: CronsOverview | null }) {
  const counts = scheduleCounts(overview?.health);
  return (
    <div className="mb-5">
      <div className="flex items-baseline justify-between mb-1">
        <h1 className="text-xl font-bold">Crons</h1>
        <span className="text-[10px] font-mono text-[var(--muted)]">
          refresh every {POLL_MS / 1000}s
        </span>
      </div>
      {counts && <div className="text-sm text-[var(--muted)]">{counts}</div>}
    </div>
  );
}

// Relative times here re-render with the page's own clock tick, so this
// takes no `now` of its own.
function HealthBannerCard({ overview }: { overview: CronsOverview | null }) {
  const health = overview?.health;
  const banner = healthBanner(health, overview?.schedules ?? []);
  const tick = health?.last_tick;
  const timer = health?.timer;
  const tone =
    banner.tone === "ok"
      ? "border-[var(--border)] bg-[var(--surface)]"
      : banner.tone === "danger"
        ? "border-red-500/40 bg-red-500/10"
        : "border-amber-500/40 bg-amber-500/10";
  const dot =
    banner.tone === "ok"
      ? "bg-[var(--success)]"
      : banner.tone === "danger"
        ? "bg-[var(--danger)]"
        : "bg-[var(--warning)]";
  return (
    <section
      aria-label="Cron health"
      className={`rounded-lg border p-4 flex items-start gap-3 ${tone}`}
    >
      <span className={`w-2.5 h-2.5 rounded-full shrink-0 mt-1.5 ${dot}`} />
      <div className="min-w-0">
        <div className="text-sm text-[var(--foreground)]">{banner.headline}</div>
        {banner.remedy && (
          <div className="text-xs text-[var(--muted)] mt-1">{banner.remedy}</div>
        )}
        <dl className="mt-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-0.5 text-[11px] font-mono text-[var(--muted)]">
          <dt>timer</dt>
          <dd className="truncate">{timer?.path || "not installed"}</dd>
          <dt>binary</dt>
          <dd className="truncate">{timer?.binary || "-"}</dd>
          <dt>last tick</dt>
          <dd>
            {tick?.at ? (
              <Tooltip content={fmtFullDate(tick.at)}>
                <span className="cursor-default">
                  {fmtAgo(tick.at)}
                  {tick.host ? ` on ${tick.host}` : ""}
                  {tick.version ? ` (${tick.version})` : ""}
                </span>
              </Tooltip>
            ) : (
              "never"
            )}
          </dd>
        </dl>
      </div>
    </section>
  );
}

function ScheduleTable({
  rows,
  now,
  selected,
  busyID,
  onSelect,
  onAct,
}: {
  rows: CronSchedule[];
  now: number;
  selected: string | null;
  busyID: string | null;
  onSelect: (id: string | null) => void;
  onAct: (s: CronSchedule, kind: "pause" | "resume" | "run") => void;
}) {
  return (
    <div className="overflow-x-auto rounded-lg border border-[var(--border)]">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-[10px] uppercase tracking-wider text-[var(--muted)] bg-[var(--surface)]">
            <Th>Name</Th>
            <Th hideSm>Cron</Th>
            <Th hideSm>TZ</Th>
            <Th>Next</Th>
            <Th>Last</Th>
            <Th>Outcome</Th>
            <Th>State</Th>
            <Th>Lock</Th>
            <Th right>Actions</Th>
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 ? (
            <tr>
              <td
                colSpan={9}
                className="px-3 py-3 text-[var(--muted)] text-center bg-[var(--surface)]"
              >
                Every schedule on this host is hidden.
              </td>
            </tr>
          ) : (
            rows.map((s) => (
              <ScheduleRow
                key={s.id}
                s={s}
                now={now}
                selected={s.id === selected}
                busy={busyID === s.id}
                onSelect={onSelect}
                onAct={onAct}
              />
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}

function ScheduleRow({
  s,
  now,
  selected,
  busy,
  onSelect,
  onAct,
}: {
  s: CronSchedule;
  now: number;
  selected: boolean;
  busy: boolean;
  onSelect: (id: string | null) => void;
  onAct: (s: CronSchedule, kind: "pause" | "resume" | "run") => void;
}) {
  return (
    <tr
      data-schedule-id={s.id}
      onClick={() => onSelect(selected ? null : s.id)}
      className={`border-t border-[var(--border)] cursor-pointer align-top ${
        selected ? "bg-[var(--surface-raised)]" : "bg-[var(--surface)]"
      }`}
    >
      <Td>
        <div className="flex items-center gap-1.5">
          <span className="font-mono text-xs text-[var(--foreground)]">
            {s.name}
          </span>
          {s.schedule_name && s.schedule_name !== "default" && (
            <span className="font-mono text-[10px] px-1 py-0.5 rounded bg-[var(--surface-raised)] text-[var(--muted)]">
              {s.schedule_name}
            </span>
          )}
        </div>
        <div className="font-mono text-[10px] text-[var(--muted)] truncate max-w-[16rem]">
          {s.repo_path}
        </div>
      </Td>
      <Td hideSm mono muted>
        <div className="flex items-center gap-1.5">
          <span>{s.effective?.cron || s.cron}</span>
          <OverridePill s={s} />
        </div>
      </Td>
      <Td hideSm mono muted>
        {s.effective?.tz || s.tz}
      </Td>
      <Td mono>
        {s.next_due_at ? (
          <Tooltip content={fmtFullDate(s.next_due_at)}>
            <span
              className={`cursor-default ${
                s.state === "armed" ? "" : "text-[var(--muted)]"
              }`}
            >
              {fmtUntil(s.next_due_at, now)}
            </span>
          </Tooltip>
        ) : (
          <span className="text-[var(--muted)]">-</span>
        )}
      </Td>
      <Td mono>
        {s.last_fired_at ? (
          <Tooltip content={fmtFullDate(s.last_fired_at)}>
            <span className="cursor-default">{fmtAgo(s.last_fired_at)}</span>
          </Tooltip>
        ) : (
          <span className="text-[var(--muted)]">never</span>
        )}
        {s.last_run_id && (
          <div>
            <RunLink id={s.last_run_id} />
          </div>
        )}
      </Td>
      <Td>
        <Pill tone={outcomeTone(s.last_outcome)} label={outcomeLabel(s.last_outcome)} />
      </Td>
      <Td>
        <Pill tone={stateTone(s.state)} label={s.state} />
      </Td>
      <Td>
        <LockCell s={s} />
      </Td>
      <Td right>
        <div className="inline-flex items-center gap-1.5">
          <RowButton
            label={s.paused ? "Resume" : "Pause"}
            busy={busy}
            onClick={() => onAct(s, s.paused ? "resume" : "pause")}
          />
          <RowButton
            label="Run now"
            busy={busy}
            onClick={() => onAct(s, "run")}
          />
          <RowButton
            label="Details"
            busy={false}
            onClick={() => onSelect(selected ? null : s.id)}
          />
        </div>
      </Td>
    </tr>
  );
}

function RowButton({
  label,
  busy,
  onClick,
}: {
  label: string;
  busy: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      disabled={busy}
      onClick={(e) => {
        e.stopPropagation();
        onClick();
      }}
      className="px-2 py-1 text-xs rounded-sm border border-[var(--border)] text-[var(--muted)] hover:text-[var(--foreground)] hover:bg-[var(--surface-raised)] disabled:opacity-40 disabled:cursor-not-allowed"
    >
      {label}
    </button>
  );
}

function OverridePill({ s }: { s: CronSchedule }) {
  const marker = overrideMarker(s);
  if (!marker) return null;
  return <Pill tone={marker.tone} label={marker.label} title={marker.title} />;
}

// The badge says what the next fire runs; the short ref rides beside it for the
// drifted states, whose label spends its room on the drift instead.
function LockCell({ s }: { s: CronSchedule }) {
  const badge = lockBadge(s.lock);
  const ref = shortRef(s.lock?.ref);
  return (
    <div className="flex flex-col items-start gap-0.5">
      <Pill
        tone={badge.tone}
        label={badge.label}
        title={s.state_detail || lockNote(s.lock)}
      />
      {ref && s.lock?.state !== "pinned" && (
        <span className="font-mono text-[10px] text-[var(--muted)]">{ref}</span>
      )}
    </div>
  );
}

function DetailPane({
  detail,
  missing,
  now,
  onClose,
}: {
  detail: CronDetail | null;
  missing: boolean;
  now: number;
  onClose: () => void;
}) {
  if (!detail) {
    return (
      <section
        aria-label="Schedule detail"
        className="rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4 text-sm text-[var(--muted)]"
      >
        {missing
          ? "That schedule is not on this host."
          : "Loading the schedule."}
      </section>
    );
  }
  const s = detail.schedule;
  return (
    <section
      aria-label="Schedule detail"
      className="rounded-lg border border-[var(--border)] bg-[var(--surface)] p-4 flex flex-col gap-4"
    >
      <div className="flex items-start justify-between gap-2">
        <h2 className="font-mono text-sm text-[var(--foreground)] break-all">
          {s.name}
        </h2>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close schedule detail"
          className="text-xs text-[var(--muted)] hover:text-[var(--foreground)]"
        >
          ✕
        </button>
      </div>

      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs">
        <Field label="id" value={s.id} />
        <Field label="repo" value={s.repo_path} />
        <Field label="pipeline" value={s.pipeline} />
        <Field label="schedule" value={s.schedule_name} />
        <Field label="where" value={s.where} />
        <Field label="state" value={s.state} />
        <Field label="paused" value={s.paused ? "yes" : "no"} />
        <Field label="declared" value={s.declared ? "yes" : "no"} />
        <Field label="armed" value={fmtDateTime(s.armed_at)} />
        <Field label="updated" value={fmtDateTime(s.updated_at)} />
        <Field
          label="next due"
          value={
            s.next_due_at
              ? `${fmtDateTime(s.next_due_at)} (${fmtUntil(s.next_due_at, now)})`
              : "-"
          }
        />
        <Field
          label="last fired"
          value={s.last_fired_at ? fmtDateTime(s.last_fired_at) : "never"}
        />
        <Field label="last outcome" value={outcomeLabel(s.last_outcome)} />
        <dt className="text-[var(--muted)]">last run</dt>
        <dd className="font-mono break-all">
          {s.last_run_id ? <RunLink id={s.last_run_id} /> : "-"}
        </dd>
      </dl>

      <CadenceTable s={s} />

      <LockBlock s={s} />

      <div>
        <SectionHeading>Upcoming</SectionHeading>
        {detail.upcoming.length === 0 ? (
          <div className="text-xs text-[var(--muted)]">
            Nothing is due; the schedule is paused or no longer declared.
          </div>
        ) : (
          <ul className="flex flex-col gap-0.5">
            {detail.upcoming.map((at) => (
              <li key={at} className="font-mono text-xs">
                {fmtDateTime(at)}{" "}
                <span className="text-[var(--muted)]">
                  {fmtUntil(at, now)}
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>

      <div>
        <SectionHeading>Fires</SectionHeading>
        <FiresTable fires={detail.fires} />
      </div>
    </section>
  );
}

/**
 * CadenceTable puts what the repo declares beside what this host runs. A row
 * this host has not overridden runs the declared value, so its effective cell
 * is a dash rather than the same string twice.
 */
function CadenceTable({ s }: { s: CronSchedule }) {
  const rows = declaredVsEffective(s);
  const fields = s.override?.fields ?? [];
  return (
    <div>
      <SectionHeading>Cadence</SectionHeading>
      <div className="overflow-x-auto rounded-lg border border-[var(--border)]">
        <table className="w-full text-xs" aria-label="Declared and effective">
          <thead>
            <tr className="text-[10px] uppercase tracking-wider text-[var(--muted)] bg-[var(--surface-raised)]">
              <Th>Field</Th>
              <Th>Declared</Th>
              <Th>Effective</Th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr
                key={r.field}
                data-cadence-field={r.field}
                className={`border-t border-[var(--border)] ${
                  r.overridden ? "bg-amber-500/10" : ""
                }`}
              >
                <Td muted>{r.label}</Td>
                <Td mono>{r.declared}</Td>
                <Td mono>
                  {r.overridden ? (
                    <span className="text-amber-400">{r.effective}</span>
                  ) : (
                    <span className="text-[var(--muted)]" title="same as declared">
                      -
                    </span>
                  )}
                </Td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {fields.length > 0 && (
        <div className="text-[11px] text-[var(--muted)] mt-1">
          {`This host overrides ${fields.join(", ")}`}
          {s.override?.set_at ? `, set ${fmtDateTime(s.override.set_at)}.` : "."}
        </div>
      )}
      {s.override?.stale && (
        <div className="text-[11px] text-amber-400 mt-1">
          The repo has changed the declaration this override was set against; it
          still applies. Re-run <code>sparkwing crons install</code> to re-base
          it.
        </div>
      )}
    </div>
  );
}

function LockBlock({ s }: { s: CronSchedule }) {
  const badge = lockBadge(s.lock);
  return (
    <div>
      <SectionHeading>Lock</SectionHeading>
      <div className="flex items-center gap-2 mb-2">
        <Pill tone={badge.tone} label={badge.label} />
        {s.state_detail && (
          <span className="text-[11px] text-[var(--muted)]">
            {s.state_detail}
          </span>
        )}
      </div>
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs">
        <Field label="ref" value={s.lock?.ref ?? ""} />
        <Field label="binary" value={s.lock?.binary ?? ""} />
        <Field label="lock state" value={s.lock?.state ?? ""} />
      </dl>
      <div className="text-[11px] text-[var(--muted)] mt-1">
        {lockNote(s.lock)}
      </div>
    </div>
  );
}

function FiresTable({ fires }: { fires: CronFire[] }) {
  if (fires.length === 0) {
    return (
      <div className="text-xs text-[var(--muted)]">
        No tick has decided this schedule yet.
      </div>
    );
  }
  return (
    <div className="overflow-x-auto rounded-lg border border-[var(--border)]">
      <table className="w-full text-xs">
        <thead>
          <tr className="text-[10px] uppercase tracking-wider text-[var(--muted)] bg-[var(--surface-raised)]">
            <Th>Due</Th>
            <Th>Decided</Th>
            <Th>Outcome</Th>
            <Th>Args</Th>
            <Th>Run</Th>
            <Th>Run status</Th>
            <Th>Detail</Th>
          </tr>
        </thead>
        <tbody>
          {fires.map((f) => (
            <tr key={f.id} className="border-t border-[var(--border)]">
              <Td mono muted>
                {fmtDateTime(f.due_at)}
              </Td>
              <Td mono muted>
                {fmtDateTime(f.decided_at)}
              </Td>
              <Td>
                <Pill tone={outcomeTone(f.outcome)} label={outcomeLabel(f.outcome)} />
              </Td>
              <Td mono muted>
                {fmtArgs(f.args)}
              </Td>
              <Td>{f.run_id ? <RunLink id={f.run_id} /> : "-"}</Td>
              <Td>
                {f.run_status ? (
                  <StatusLabel status={f.run_status} />
                ) : (
                  <span className="text-[var(--muted)]">-</span>
                )}
              </Td>
              <Td muted>{f.detail || "-"}</Td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <>
      <dt className="text-[var(--muted)]">{label}</dt>
      <dd className="font-mono break-all">{value || "-"}</dd>
    </>
  );
}

function SectionHeading({ children }: { children: React.ReactNode }) {
  return (
    <h3 className="text-xs font-bold uppercase tracking-wider text-[var(--muted)] mb-2">
      {children}
    </h3>
  );
}

function Pill({
  tone,
  label,
  title,
}: {
  tone: CronTone;
  label: string;
  title?: string;
}) {
  const cls =
    tone === "ok"
      ? "bg-green-500/15 text-green-400"
      : tone === "warning"
        ? "bg-yellow-500/15 text-yellow-400"
        : tone === "danger"
          ? "bg-red-500/15 text-red-400"
          : "bg-gray-500/15 text-gray-400";
  return (
    <span
      title={title}
      className={`inline-block px-2 py-0.5 rounded text-xs font-mono ${cls}`}
    >
      {label}
    </span>
  );
}

function RunLink({ id }: { id: string }) {
  return (
    <Link
      href={`/runs?run=${encodeURIComponent(id)}`}
      onClick={(e) => e.stopPropagation()}
      className="font-mono text-xs text-violet-300 hover:underline"
    >
      {id}
    </Link>
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
          No schedules are armed on this host.
        </div>
        <div className="text-xs text-[var(--muted)] mt-1">
          Declare <code className="font-mono">on: schedule:</code> in a repo&apos;s{" "}
          <code className="font-mono">sparkwing.yaml</code>, then run{" "}
          <code className="font-mono">sparkwing crons install</code> in that repo
          to arm it here.
        </div>
      </div>
    </div>
  );
}
