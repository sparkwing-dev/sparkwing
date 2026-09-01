"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import {
  type CapacityChargeStep,
  type CapacityExplain,
  type CapacityProfile,
  type CapacityProfiles,
  type CapacityRankSelection,
  type QueueHolder,
  type QueueResource,
  type QueueState,
  type QueueWaiter,
  getCapacityExplain,
  getCapacityProfiles,
  getQueue,
} from "@/lib/api";
import {
  availabilityResidual,
  availabilityTerms,
  daemonUptimeLabel,
  externalCell,
  externalPressureNote,
  externalUnmeasuredNote,
  fmtAmount,
  fmtCost,
  fmtDuration,
  fmtHolderCost,
  groupHolders,
  hasDaemon,
  humanBytes,
  queueLifecycleHolders,
  queueRowID,
  resourceAvailable,
  trimFloat,
} from "@/lib/queue";
import {
  type CapacitySortKey,
  chargeBasisLabel,
  mismatchNote,
  pinDriftFlag,
  rankLabel,
  sortProfiles,
} from "@/lib/capacity";
import Tooltip from "@/components/Tooltip";

const HOST_POLL_MS = 2000;
const PRICING_POLL_MS = 10000;

export default function CapacityPage() {
  const [qs, setQs] = useState<QueueState | null>(null);
  const [hostLoaded, setHostLoaded] = useState(false);
  const [pricing, setPricing] = useState<CapacityProfiles | null>(null);
  const [pricingLoaded, setPricingLoaded] = useState(false);
  const [selected, setSelected] = useState<string>("");
  const [explain, setExplain] = useState<CapacityExplain | null>(null);
  const [sortKey, setSortKey] = useState<CapacitySortKey>("charge");
  const [ascending, setAscending] = useState(false);
  const [pulse, setPulse] = useState(false);
  const pulseTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const refreshHost = useCallback(async () => {
    setQs(await getQueue());
    setHostLoaded(true);
    setPulse(true);
    if (pulseTimer.current) clearTimeout(pulseTimer.current);
    pulseTimer.current = setTimeout(() => setPulse(false), 600);
  }, []);

  const refreshPricing = useCallback(async () => {
    setPricing(await getCapacityProfiles());
    setPricingLoaded(true);
  }, []);

  useEffect(() => {
    let cancelled = false;
    queueMicrotask(() => {
      if (!cancelled) void refreshHost();
    });
    const i = window.setInterval(() => {
      if (!document.hidden) refreshHost();
    }, HOST_POLL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(i);
      if (pulseTimer.current) clearTimeout(pulseTimer.current);
    };
  }, [refreshHost]);

  useEffect(() => {
    let cancelled = false;
    queueMicrotask(() => {
      if (!cancelled) void refreshPricing();
    });
    const i = window.setInterval(() => {
      if (!document.hidden) refreshPricing();
    }, PRICING_POLL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(i);
    };
  }, [refreshPricing]);

  useEffect(() => {
    if (!selected) {
      let cancelled = false;
      queueMicrotask(() => {
        if (!cancelled) setExplain(null);
      });
      return () => {
        cancelled = true;
      };
    }
    let cancelled = false;
    async function load() {
      const next = await getCapacityExplain(selected);
      if (!cancelled) setExplain(next);
    }
    load();
    const i = window.setInterval(() => {
      if (!document.hidden) load();
    }, PRICING_POLL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(i);
    };
  }, [selected]);

  const rows = useMemo(
    () => sortProfiles(pricing?.profiles ?? [], sortKey, ascending),
    [pricing, sortKey, ascending],
  );

  const onSort = useCallback(
    (key: CapacitySortKey) => {
      if (key === sortKey) {
        setAscending((v) => !v);
        return;
      }
      setSortKey(key);
      setAscending(key === "pipeline");
    },
    [sortKey],
  );

  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-6xl mx-auto w-full">
      <Header qs={qs} pulse={pulse} />
      <div className="flex flex-col gap-8">
        <HostSection qs={qs} loaded={hostLoaded} />
        <PricingSection
          pricing={pricing}
          loaded={pricingLoaded}
          rows={rows}
          sortKey={sortKey}
          ascending={ascending}
          onSort={onSort}
          selected={selected}
          onSelect={(pipeline) =>
            setSelected((cur) => (cur === pipeline ? "" : pipeline))
          }
        />
        <ExplainSection
          selected={selected}
          explain={explain}
          hasPricing={(pricing?.profiles.length ?? 0) > 0}
        />
      </div>
    </div>
  );
}

function Header({ qs, pulse }: { qs: QueueState | null; pulse: boolean }) {
  const running = qs != null && hasDaemon(qs);
  const uptime = qs ? daemonUptimeLabel(qs) : "";
  return (
    <div className="mb-5">
      <div className="flex items-baseline justify-between mb-1">
        <div className="flex items-center gap-2">
          <h1 className="text-xl font-bold">Capacity</h1>
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
          host every {HOST_POLL_MS / 1000}s · pricing every{" "}
          {PRICING_POLL_MS / 1000}s
        </span>
      </div>
      <div className="text-sm text-[var(--muted)]">
        What admission is charging on this machine, and the arithmetic behind
        every figure.
      </div>
      {running && (qs?.daemon_version || uptime) ? (
        <div className="text-[11px] font-mono text-[var(--muted)] mt-0.5">
          daemon {qs?.daemon_version || "unknown"}
          {uptime ? `, ${uptime}` : ""}
        </div>
      ) : null}
    </div>
  );
}

function HostSection({
  qs,
  loaded,
}: {
  qs: QueueState | null;
  loaded: boolean;
}) {
  if (!loaded) return <Skeleton title="Host, live" />;
  if (!qs || !hasDaemon(qs)) {
    return (
      <Section
        title="Host, live"
        hint="The ledger admission grants against, as the daemon sees it."
      >
        <Empty>
          <div className="text-sm text-[var(--foreground)]">
            Daemon not running; nothing is being admitted or charged.
          </div>
          <div className="text-xs text-[var(--muted)] mt-1">
            The daemon starts with your next run and arbitrates local capacity
            from there. Learned pricing below is read from the runs store and
            stays available meanwhile.
          </div>
        </Empty>
      </Section>
    );
  }
  const waiters = qs.waiters ?? [];
  const holders = groupHolders(
    queueLifecycleHolders(qs.holders ?? [], waiters),
  );
  const unmeasured = externalUnmeasuredNote(qs);
  const pressure = externalPressureNote(qs);
  const sampleAge = qs.external_sample_age_ms ?? 0;
  return (
    <Section
      title="Host, live"
      hint="The ledger admission grants against, as the daemon sees it."
    >
      <div className="overflow-x-auto rounded-lg border border-[var(--border)]">
        <table className="w-full text-sm">
          <thead>
            <tr className="text-[10px] uppercase tracking-wider text-[var(--muted)] bg-[var(--surface)]">
              <Th>Resource</Th>
              <Th right>Capacity</Th>
              <Th right>Held</Th>
              <Th right>Reserved</Th>
              <Th right>External</Th>
              <Th right>Available</Th>
            </tr>
          </thead>
          <tbody>
            {(qs.resources ?? []).length === 0 ? (
              <tr>
                <td
                  colSpan={6}
                  className="px-3 py-3 text-[var(--muted)] text-center bg-[var(--surface)]"
                >
                  No resource dimensions reported.
                </td>
              </tr>
            ) : (
              (qs.resources ?? []).flatMap((r) => {
                const terms = availabilityTerms(r);
                const residual = availabilityResidual(
                  r,
                  qs.ignore_external ?? false,
                );
                const available = resourceAvailable(r);
                const rows = [
                  <tr
                    key={r.key}
                    className="border-t border-[var(--border)] bg-[var(--surface)]"
                  >
                    <Td>
                      <span className="font-mono">{r.key}</span>
                    </Td>
                    <Td right mono>
                      {fmtAmount(r.key, r.capacity)}
                    </Td>
                    <Td right mono>
                      {fmtAmount(r.key, r.held)}
                    </Td>
                    <Td right mono muted>
                      {r.key === "cores" || r.key === "memory"
                        ? fmtAmount(r.key, r.reserved ?? 0)
                        : "-"}
                    </Td>
                    <Td right mono muted>
                      <ExternalCell r={r} />
                    </Td>
                    <Td right mono>
                      <span
                        className={
                          available <= 0
                            ? "text-[var(--warning)]"
                            : "text-[var(--success)]"
                        }
                      >
                        {fmtAmount(r.key, available)}
                      </span>
                    </Td>
                  </tr>,
                ];
                if (terms.length > 0) {
                  rows.push(
                    <tr key={`${r.key}-math`} className="bg-[var(--surface)]">
                      <td
                        colSpan={6}
                        className="px-3 pb-2 text-[11px] font-mono text-[var(--muted)]"
                      >
                        {terms
                          .map((t) =>
                            t.sign ? `${t.sign} ${t.value}` : t.value,
                          )
                          .join(" ")}{" "}
                        = {fmtAmount(r.key, available)}
                        {residual != null &&
                        Math.abs(residual - available) > 0.001 ? (
                          <span className="ml-2 text-[var(--warning)]">
                            (operands leave {fmtAmount(r.key, residual)}; the
                            daemon reports {fmtAmount(r.key, available)})
                          </span>
                        ) : null}
                      </td>
                    </tr>,
                  );
                }
                return rows;
              })
            )}
          </tbody>
        </table>
      </div>
      {sampleAge > 0 && (
        <div className="mt-2 text-[11px] font-mono text-[var(--muted)]">
          external reading applied {fmtDuration(sampleAge)} ago
          {qs.ignore_external ? "; admission is configured to ignore it" : ""}
        </div>
      )}
      {unmeasured && (
        <div className="mt-2">
          <Callout tone="warning">{unmeasured}</Callout>
        </div>
      )}
      {pressure && (
        <div className="mt-2">
          <Callout tone="warning">{pressure}</Callout>
        </div>
      )}

      <SubTitle>Holding now</SubTitle>
      <div className="overflow-x-auto rounded-lg border border-[var(--border)]">
        <table className="w-full text-sm">
          <thead>
            <tr className="text-[10px] uppercase tracking-wider text-[var(--muted)] bg-[var(--surface)]">
              <Th>Run</Th>
              <Th hideSm>Pipeline</Th>
              <Th right>Elapsed</Th>
              <Th>Charged</Th>
              <Th>Why that charge</Th>
            </tr>
          </thead>
          <tbody>
            {holders.length === 0 ? (
              <tr>
                <td
                  colSpan={5}
                  className="px-3 py-3 text-[var(--muted)] text-center bg-[var(--surface)]"
                >
                  Nothing is holding capacity.
                </td>
              </tr>
            ) : (
              holders.flatMap((g) => [
                <HolderRow key={queueRowID(g.holder)} h={g.holder} />,
                ...g.children.map((c) => (
                  <HolderRow key={queueRowID(c)} h={c} attached />
                )),
              ])
            )}
          </tbody>
        </table>
      </div>

      <SubTitle>Waiting</SubTitle>
      <div className="overflow-x-auto rounded-lg border border-[var(--border)]">
        <table className="w-full text-sm">
          <thead>
            <tr className="text-[10px] uppercase tracking-wider text-[var(--muted)] bg-[var(--surface)]">
              <Th right>#</Th>
              <Th>Run</Th>
              <Th hideSm>Pipeline</Th>
              <Th>Needs</Th>
              <Th>Blocked because</Th>
              <Th right hideSm>
                Waited
              </Th>
            </tr>
          </thead>
          <tbody>
            {waiters.length === 0 ? (
              <tr>
                <td
                  colSpan={6}
                  className="px-3 py-3 text-[var(--muted)] text-center bg-[var(--surface)]"
                >
                  No one is queued.
                </td>
              </tr>
            ) : (
              waiters.map((w) => <WaiterRow key={queueRowID(w)} w={w} />)
            )}
          </tbody>
        </table>
      </div>
    </Section>
  );
}

function ExternalCell({ r }: { r: QueueResource }) {
  const label = externalCell(r, fmtAmount);
  if (!r.external_source) return <>{label}</>;
  return (
    <Tooltip
      content={
        r.external_source === "unmeasured"
          ? "No host sensor for this dimension; nothing was subtracted for external load"
          : "Measured from the host, outside sparkwing"
      }
    >
      <span className="cursor-default">{label}</span>
    </Tooltip>
  );
}

function HolderRow({ h, attached }: { h: QueueHolder; attached?: boolean }) {
  return (
    <tr className="border-t border-[var(--border)] bg-[var(--surface)] align-top">
      <Td>
        <div className={attached ? "pl-4 flex items-center gap-1.5" : ""}>
          {attached && (
            <span className="text-[var(--muted)] text-xs" aria-hidden="true">
              ↳
            </span>
          )}
          <RunLink id={h.run_id} label={h.display_run_id || h.run_id} />
          {h.connection_only && (
            <Tooltip content="Keeps the run connected for lifecycle and finalization; holds no host resource">
              <span>
                <Chip tone="neutral">lifecycle</Chip>
              </span>
            </Tooltip>
          )}
          {h.stalled && <Chip tone="danger">stalled</Chip>}
          {!h.stalled && h.contended && <Chip tone="warning">contended</Chip>}
        </div>
      </Td>
      <Td hideSm mono muted>
        {h.pipeline || "-"}
      </Td>
      <Td right mono>
        {fmtDuration(h.elapsed_ms)}
      </Td>
      <Td mono>{h.connection_only ? "-" : fmtHolderCost(h)}</Td>
      <Td>
        {h.connection_only ? (
          <span className="text-xs text-[var(--muted)]">
            charged nothing; draws no budget
          </span>
        ) : (
          <Rationale source={h.cost_source} rationale={h.cost_rationale} />
        )}
      </Td>
    </tr>
  );
}

function WaiterRow({ w }: { w: QueueWaiter }) {
  return (
    <tr className="border-t border-[var(--border)] bg-[var(--surface)] align-top">
      <Td right mono muted>
        {w.position}
      </Td>
      <Td>
        <RunLink id={w.run_id} label={w.display_run_id || w.run_id} />
      </Td>
      <Td hideSm mono muted>
        {w.pipeline || "-"}
      </Td>
      <Td mono>
        <div>{fmtCost(w.resources)}</div>
        <Rationale source={w.cost_source} rationale={w.cost_rationale} />
      </Td>
      <Td>
        {w.blocking_reason ? (
          <span className="text-xs text-[var(--warning)]">
            {w.blocking_reason}
          </span>
        ) : (
          <span className="text-xs text-[var(--muted)]">
            arrival order behind a heavier request
          </span>
        )}
      </Td>
      <Td right mono muted hideSm>
        {fmtDuration(w.waiting_ms ?? 0)}
      </Td>
    </tr>
  );
}

function Rationale({
  source,
  rationale,
}: {
  source?: string;
  rationale?: string;
}) {
  if (!source && !rationale) {
    return <span className="text-xs text-[var(--muted)]">unrecorded</span>;
  }
  return (
    <span className="text-xs text-[var(--muted)]">
      {rationale || source}
      {rationale && source ? (
        <span className="ml-1.5 font-mono text-[10px] px-1 py-0.5 rounded bg-[var(--surface-raised)]">
          {source}
        </span>
      ) : null}
    </span>
  );
}

function PricingSection({
  pricing,
  loaded,
  rows,
  sortKey,
  ascending,
  onSort,
  selected,
  onSelect,
}: {
  pricing: CapacityProfiles | null;
  loaded: boolean;
  rows: CapacityProfile[];
  sortKey: CapacitySortKey;
  ascending: boolean;
  onSort: (key: CapacitySortKey) => void;
  selected: string;
  onSelect: (pipeline: string) => void;
}) {
  if (!loaded) return <Skeleton title="Pipelines, priced" />;
  if (!pricing) {
    return (
      <Section title="Pipelines, priced">
        <Empty>
          <div className="text-sm text-[var(--foreground)]">
            Learned pricing is unavailable here.
          </div>
          <div className="text-xs text-[var(--muted)] mt-1">
            Profiles live in the local runs store, so this table appears when
            the dashboard is reading one.
          </div>
        </Empty>
      </Section>
    );
  }
  if (rows.length === 0) {
    return (
      <Section title="Pipelines, priced">
        <Empty>
          <div className="text-sm text-[var(--foreground)]">
            Nothing has been measured yet.
          </div>
          <div className="text-xs text-[var(--muted)] mt-1">
            A pipeline is priced after {pricing.constants.min_samples} clean
            runs; until then it is charged the cold-start default of{" "}
            {trimFloat(pricing.constants.cold_start_cores)} cores.
          </div>
        </Empty>
      </Section>
    );
  }
  return (
    <Section
      title="Pipelines, priced"
      hint={`What ${rows.length} measured pipeline${rows.length === 1 ? "" : "s"} would be charged on this ${pricing.machine_cores}-core machine. Click a row for its derivation.`}
    >
      <div className="overflow-x-auto rounded-lg border border-[var(--border)]">
        <table className="w-full text-sm">
          <thead>
            <tr className="text-[10px] uppercase tracking-wider text-[var(--muted)] bg-[var(--surface)]">
              <SortTh
                label="Pipeline"
                col="pipeline"
                sortKey={sortKey}
                ascending={ascending}
                onSort={onSort}
              />
              <SortTh
                label="CPU charge"
                col="charge"
                right
                sortKey={sortKey}
                ascending={ascending}
                onSort={onSort}
              />
              <Th>From</Th>
              <SortTh
                label="Mem charge"
                col="memory"
                right
                sortKey={sortKey}
                ascending={ascending}
                onSort={onSort}
              />
              <Th right hideSm>
                Peak / sustained
              </Th>
              <Th right hideSm>
                Floor
              </Th>
              <SortTh
                label="Samples"
                col="samples"
                right
                sortKey={sortKey}
                ascending={ascending}
                onSort={onSort}
              />
              <SortTh
                label="p50 / p99"
                col="duration"
                right
                sortKey={sortKey}
                ascending={ascending}
                onSort={onSort}
              />
            </tr>
          </thead>
          <tbody>
            {rows.map((p) => (
              <tr
                key={p.pipeline}
                onClick={() => onSelect(p.pipeline)}
                className={`border-t border-[var(--border)] cursor-pointer hover:bg-[var(--surface-raised)] ${
                  selected === p.pipeline
                    ? "bg-[var(--surface-raised)]"
                    : "bg-[var(--surface)]"
                }`}
              >
                <Td>
                  <span className="font-mono text-xs">
                    {p.display || p.pipeline}
                  </span>
                  <PinFlag p={p} />
                </Td>
                <Td right mono>
                  {trimFloat(p.charge.cores)}
                </Td>
                <Td mono muted>
                  <Tooltip content={p.charge.rationale || p.charge.source}>
                    <span className="cursor-default text-[11px]">
                      {chargeBasisLabel(p.charge.cores_basis)}
                      {p.charge.floor_applied ? " (floored)" : ""}
                    </span>
                  </Tooltip>
                </Td>
                <Td right mono>
                  {p.charge.memory_bytes > 0
                    ? humanBytes(p.charge.memory_bytes)
                    : "-"}
                </Td>
                <Td right mono muted hideSm>
                  {trimFloat(p.peak_cores)} / {trimFloat(p.sustained_cores)}
                </Td>
                <Td right mono muted hideSm>
                  {(p.floor_cores ?? 0) > 0
                    ? trimFloat(p.floor_cores ?? 0)
                    : "-"}
                </Td>
                <Td right mono muted>
                  {p.sample_count}
                </Td>
                <Td right mono muted>
                  {fmtDuration(p.p50_duration_ms)} /{" "}
                  {fmtDuration(p.p99_duration_ms)}
                </Td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {rows
        .filter((p) => p.drift)
        .map((p) => (
          <div key={`drift-${p.pipeline}`} className="mt-2">
            <Callout tone="warning">
              <span className="font-mono text-violet-300">
                {p.display || p.pipeline}
              </span>
              : {p.drift}
            </Callout>
          </div>
        ))}
    </Section>
  );
}

function PinFlag({ p }: { p: CapacityProfile }) {
  const flag = pinDriftFlag(p);
  if (!flag) return null;
  const tone = flag === "pinned" ? "neutral" : "warning";
  return (
    <Tooltip
      content={
        p.drift ||
        `Pinned at ${trimFloat(p.pinned_cores ?? 0)} cores by .Resources()`
      }
    >
      <span className="ml-1.5 align-middle">
        <Chip tone={tone}>{flag}</Chip>
      </span>
    </Tooltip>
  );
}

function ExplainSection({
  selected,
  explain,
  hasPricing,
}: {
  selected: string;
  explain: CapacityExplain | null;
  hasPricing: boolean;
}) {
  if (!selected) {
    if (!hasPricing) return null;
    return (
      <Section title="Show the work">
        <Empty>
          <div className="text-sm text-[var(--foreground)]">
            Pick a pipeline above.
          </div>
          <div className="text-xs text-[var(--muted)] mt-1">
            Its sample window appears here with the run each charge was ranked
            out of marked, so the price can be checked by hand.
          </div>
        </Empty>
      </Section>
    );
  }
  if (!explain) return <Skeleton title="Show the work" />;

  const sel = explain.selections;
  const notes = [
    mismatchNote(sel.cores, "core charge"),
    mismatchNote(sel.memory, "memory charge"),
  ].filter(Boolean);

  return (
    <Section
      title="Show the work"
      hint={`How ${explain.profile.display || explain.profile.pipeline} is priced, from the ${explain.samples.length} runs still in its window.`}
    >
      <ChainList chain={explain.chain} />
      <div className="mt-2 text-[11px] text-[var(--muted)]">
        {explain.ceiling_note}
      </div>

      <SubTitle>Sample window</SubTitle>
      <div className="text-[11px] text-[var(--muted)] mb-2">
        Oldest run first. Cores are charged at {rankLabel(sel.cores)} of each
        run&apos;s sustained draw (itself the p
        {Math.round(explain.constants.sustained_percentile * 100)} of that
        run&apos;s readings, never below its mean); memory at{" "}
        {rankLabel(sel.memory)} of the per-run peaks.
      </div>
      <div className="overflow-x-auto rounded-lg border border-[var(--border)]">
        <table className="w-full text-sm">
          <thead>
            <tr className="text-[10px] uppercase tracking-wider text-[var(--muted)] bg-[var(--surface)]">
              <Th right>Run</Th>
              <Th right>Duration</Th>
              <Th right>Peak cores</Th>
              <Th right>Sustained cores</Th>
              <Th right>Peak memory</Th>
              <Th>Selected</Th>
            </tr>
          </thead>
          <tbody>
            {explain.samples.length === 0 ? (
              <tr>
                <td
                  colSpan={6}
                  className="px-3 py-3 text-[var(--muted)] text-center bg-[var(--surface)]"
                >
                  No stored samples; this pipeline is priced without
                  measurement.
                </td>
              </tr>
            ) : (
              explain.samples.map((s) => {
                const marks = [
                  sel.cores.index === s.index ? "cores" : "",
                  sel.memory.index === s.index ? "memory" : "",
                  sel.duration_p50.index === s.index ? "p50" : "",
                  sel.duration_p99.index === s.index ? "p99" : "",
                ].filter(Boolean);
                const chargeMark =
                  sel.cores.index === s.index || sel.memory.index === s.index;
                return (
                  <tr
                    key={s.index}
                    className={`border-t border-[var(--border)] ${
                      chargeMark
                        ? "bg-[var(--surface-raised)]"
                        : "bg-[var(--surface)]"
                    }`}
                  >
                    <Td right mono muted>
                      {s.index + 1}
                    </Td>
                    <Td right mono muted>
                      {fmtDuration(s.duration_ms)}
                    </Td>
                    <Td right mono muted>
                      {trimFloat(s.peak_cores)}
                    </Td>
                    <Td right mono>
                      <span
                        className={
                          sel.cores.index === s.index
                            ? "text-[var(--success)] font-bold"
                            : ""
                        }
                      >
                        {trimFloat(s.sustained_cores)}
                      </span>
                    </Td>
                    <Td right mono>
                      <span
                        className={
                          sel.memory.index === s.index
                            ? "text-[var(--success)] font-bold"
                            : ""
                        }
                      >
                        {humanBytes(s.peak_memory_bytes)}
                      </span>
                    </Td>
                    <Td>
                      <div className="flex flex-wrap gap-1">
                        {marks.map((m) => (
                          <Chip
                            key={m}
                            tone={
                              m === "cores" || m === "memory"
                                ? "success"
                                : "neutral"
                            }
                          >
                            {m}
                          </Chip>
                        ))}
                      </div>
                    </Td>
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>
      <div className="mt-2 flex flex-col gap-1 text-[11px] font-mono text-[var(--muted)]">
        <SelectionLine label="cores" sel={sel.cores} unit="cores" />
        <SelectionLine label="memory" sel={sel.memory} unit="bytes" />
      </div>
      {notes.map((n) => (
        <div key={n} className="mt-2">
          <Callout tone="danger">{n}</Callout>
        </div>
      ))}

      {explain.nodes.length > 0 && (
        <>
          <SubTitle>Nodes measured under this pipeline</SubTitle>
          <div className="overflow-x-auto rounded-lg border border-[var(--border)]">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-[10px] uppercase tracking-wider text-[var(--muted)] bg-[var(--surface)]">
                  <Th>Node</Th>
                  <Th right>Samples</Th>
                  <Th right>Peak cores</Th>
                  <Th right>Sustained</Th>
                  <Th right>Peak memory</Th>
                  <Th right>p50 / p99</Th>
                </tr>
              </thead>
              <tbody>
                {explain.nodes.map((n) => (
                  <tr
                    key={n.node_id}
                    className="border-t border-[var(--border)] bg-[var(--surface)]"
                  >
                    <Td mono>{n.node_id}</Td>
                    <Td right mono muted>
                      {n.sample_count}
                    </Td>
                    <Td right mono muted>
                      {trimFloat(n.peak_cores)}
                    </Td>
                    <Td right mono muted>
                      {trimFloat(n.sustained_cores)}
                    </Td>
                    <Td right mono muted>
                      {humanBytes(n.peak_memory_bytes)}
                    </Td>
                    <Td right mono muted>
                      {fmtDuration(n.p50_duration_ms)} /{" "}
                      {fmtDuration(n.p99_duration_ms)}
                    </Td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </Section>
  );
}

function SelectionLine({
  label,
  sel,
  unit,
}: {
  label: string;
  sel: CapacityRankSelection;
  unit: string;
}) {
  if (sel.unmeasured || sel.count === 0) {
    return <div>{label}: no samples to rank</div>;
  }
  const value = unit === "bytes" ? humanBytes(sel.value) : trimFloat(sel.value);
  return (
    <div>
      {label}: {rankLabel(sel)} → run {sel.index + 1} → {value}
      {sel.matches ? "" : " (does not match the stored charge)"}
    </div>
  );
}

function ChainList({ chain }: { chain: CapacityChargeStep[] }) {
  return (
    <ol className="flex flex-col gap-1.5">
      {chain.map((step) => (
        <li
          key={step.step}
          className={`rounded-lg border px-3 py-2 text-sm ${
            step.applied
              ? "border-[var(--accent)] bg-[var(--surface-raised)]"
              : "border-[var(--border)] bg-[var(--surface)] opacity-70"
          }`}
        >
          <div className="flex items-baseline justify-between gap-3">
            <span className="flex items-center gap-2">
              <span className="font-medium">{step.label}</span>
              {step.applied ? (
                <Chip tone="success">charged</Chip>
              ) : step.eligible ? (
                <Chip tone="neutral">available</Chip>
              ) : (
                <Chip tone="neutral">n/a</Chip>
              )}
            </span>
            <span className="font-mono text-xs whitespace-nowrap">
              {(step.cores ?? 0) > 0
                ? `${trimFloat(step.cores ?? 0)} cores`
                : "-"}
              {(step.memory_bytes ?? 0) > 0
                ? `, ${humanBytes(step.memory_bytes ?? 0)}`
                : ""}
            </span>
          </div>
          {step.detail && (
            <div className="text-[11px] text-[var(--muted)] mt-0.5">
              {step.detail}
            </div>
          )}
        </li>
      ))}
    </ol>
  );
}

function Section({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <h2 className="text-xs font-bold uppercase tracking-wider text-[var(--muted)] mb-1">
        {title}
      </h2>
      {hint && <div className="text-xs text-[var(--muted)] mb-2">{hint}</div>}
      {children}
    </div>
  );
}

function SubTitle({ children }: { children: React.ReactNode }) {
  return (
    <h3 className="text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mt-4 mb-2">
      {children}
    </h3>
  );
}

function Empty({ children }: { children: React.ReactNode }) {
  return (
    <div className="rounded-lg border border-[var(--border)] bg-[var(--surface)] p-6 flex items-start gap-3">
      <span className="w-2.5 h-2.5 rounded-full bg-[var(--muted)] shrink-0 mt-1.5" />
      <div>{children}</div>
    </div>
  );
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

function Chip({
  tone,
  children,
}: {
  tone: "success" | "warning" | "danger" | "neutral";
  children: React.ReactNode;
}) {
  const cls = {
    success: "bg-green-500/15 text-green-400",
    warning: "bg-amber-500/15 text-amber-300",
    danger: "bg-red-500/15 text-red-400",
    neutral: "bg-[var(--surface-raised)] text-[var(--muted)]",
  }[tone];
  return (
    <span
      className={`text-[10px] font-mono px-1.5 py-0.5 rounded cursor-default ${cls}`}
    >
      {children}
    </span>
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

function SortTh({
  label,
  col,
  right,
  sortKey,
  ascending,
  onSort,
}: {
  label: string;
  col: CapacitySortKey;
  right?: boolean;
  sortKey: CapacitySortKey;
  ascending: boolean;
  onSort: (key: CapacitySortKey) => void;
}) {
  const active = sortKey === col;
  return (
    <th
      className={`px-3 py-2 font-bold ${right ? "text-right" : "text-left"}`}
      aria-sort={active ? (ascending ? "ascending" : "descending") : "none"}
    >
      <button
        type="button"
        onClick={() => onSort(col)}
        className={`uppercase tracking-wider hover:text-[var(--foreground)] ${
          active ? "text-[var(--foreground)]" : ""
        }`}
      >
        {label}
        {active ? (ascending ? " ↑" : " ↓") : ""}
      </button>
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

function Skeleton({ title }: { title: string }) {
  return (
    <Section title={title}>
      <div className="flex flex-col gap-4 animate-pulse">
        {[0, 1].map((i) => (
          <div
            key={i}
            className="h-24 rounded-lg border border-[var(--border)] bg-[var(--surface)]"
          />
        ))}
      </div>
    </Section>
  );
}
