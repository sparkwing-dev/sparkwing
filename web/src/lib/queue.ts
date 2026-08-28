
import type {
  HostResources,
  QueueEvents,
  QueueHolder,
  QueueResource,
  QueueState,
  QueueWaiter,
} from "./api";

export function isHostResource(key: string): boolean {
  return key === "cores" || key === "memory";
}

export function trimFloat(v: number): string {
  if (Number.isInteger(v)) return String(v);
  return v.toFixed(2);
}

export function humanBytes(n: number): string {
  const kib = 1 << 10;
  const mib = 1 << 20;
  const gib = 1 << 30;
  if (n >= gib) return `${(n / gib).toFixed(1)} GiB`;
  if (n >= mib) return `${(n / mib).toFixed(1)} MiB`;
  if (n >= kib) return `${(n / kib).toFixed(1)} KiB`;
  return `${Math.round(n)} B`;
}

export function fmtAmount(key: string, v: number): string {
  if (key === "memory") return humanBytes(v);
  return trimFloat(v);
}

export function fmtCost(r: HostResources | undefined): string {
  const cores = r?.cores ?? 0;
  let out = `${trimFloat(cores)} cores`;
  if (r?.memory_bytes && r.memory_bytes > 0) {
    out += `, ${humanBytes(r.memory_bytes)}`;
  }
  return out;
}

export function fmtHolderCost(h: QueueHolder): string {
  if (h.parent) return "-";
  return fmtCost(h.resources);
}

export function queueRowID(row: {
  run_id: string;
  participant_id?: string;
}): string {
  return row.participant_id || row.run_id;
}

export function fmtDuration(ms: number): string {
  if (!ms || ms <= 0) return "-";
  const totalSec = Math.round(ms / 1000);
  if (totalSec < 60) return `${totalSec}s`;
  if (totalSec < 3600) {
    const m = Math.floor(totalSec / 60);
    const s = totalSec % 60;
    return s ? `${m}m ${s}s` : `${m}m`;
  }
  const h = Math.floor(totalSec / 3600);
  const m = Math.floor((totalSec % 3600) / 60);
  return m ? `${h}h ${m}m` : `${h}h`;
}

export function fmtETA(ms: number | null | undefined): string {
  if (ms == null) return "-";
  if (ms <= 0) return "now";
  return fmtDuration(ms);
}

export function resourceAvailable(r: QueueResource): number {
  const reserved = r.reserved ?? 0;
  const external = r.external ?? 0;
  const available = r.available ?? 0;
  if (
    isHostResource(r.key) &&
    (available > 0 || reserved > 0 || external > 0 || !!r.external_source)
  ) {
    return available;
  }
  const free = r.capacity - r.held;
  return free < 0 ? 0 : free;
}

export const EXTERNAL_UNMEASURED = "unmeasured";

export function externalCell(
  r: QueueResource,
  fmtAmount: (key: string, v: number) => string,
): string {
  if (!isHostResource(r.key)) return "-";
  if (r.external_source === EXTERNAL_UNMEASURED) return "unmeasured";
  return fmtAmount(r.key, r.external ?? 0);
}

export interface AvailabilityTerm {
  label: string;
  value: string;
  sign: "" | "-";
}

export function availabilityTerms(r: QueueResource): AvailabilityTerm[] {
  if (!isHostResource(r.key)) return [];
  const terms: AvailabilityTerm[] = [
    { label: "capacity", value: fmtAmount(r.key, r.capacity), sign: "" },
    { label: "held", value: fmtAmount(r.key, r.held), sign: "-" },
  ];
  if ((r.reserved ?? 0) > 0) {
    terms.push({
      label: "reserved",
      value: fmtAmount(r.key, r.reserved ?? 0),
      sign: "-",
    });
  }
  if (r.external_source === EXTERNAL_UNMEASURED) {
    terms.push({ label: "external", value: "unmeasured", sign: "-" });
  } else if ((r.external ?? 0) > 0) {
    terms.push({
      label: "external",
      value: fmtAmount(r.key, r.external ?? 0),
      sign: "-",
    });
  }
  return terms;
}

export function availabilityResidual(
  r: QueueResource,
  ignoreExternal: boolean,
): number | null {
  if (!isHostResource(r.key)) return null;
  if (ignoreExternal || r.external_source === EXTERNAL_UNMEASURED) return null;
  return r.capacity - r.held - (r.reserved ?? 0) - (r.external ?? 0);
}

export function externalUnmeasuredNote(qs: QueueState): string {
  if (qs.ignore_external) return "";
  const keys = (qs.resources ?? [])
    .filter(
      (r) => isHostResource(r.key) && r.external_source === EXTERNAL_UNMEASURED,
    )
    .map((r) => r.key);
  if (keys.length === 0) return "";
  return `External load is unmeasured on ${keys.join(", ")} (host sensor unavailable); none was subtracted from available.`;
}

export function externalPressureNote(qs: QueueState): string {
  if (qs.ignore_external) return "";
  const waiters = qs.waiters ?? [];
  if (waiters.length === 0) return "";
  const anyBlocked = waiters.some((w) => !!w.blocking_reason);
  if (!anyBlocked) return "";
  for (const r of qs.resources ?? []) {
    if (isHostResource(r.key) && (r.external ?? 0) > 0 && r.held < r.capacity) {
      return "External (non-sparkwing) load is the binding constraint; the free capacity above is reserved or already in use by other processes.";
    }
  }
  return "";
}

export interface HolderGroup {
  holder: QueueHolder;
  children: QueueHolder[];
}

export function groupHolders(holders: QueueHolder[]): HolderGroup[] {
  const byRun = new Map<string, QueueHolder>();
  for (const h of holders) byRun.set(queueRowID(h), h);
  const childrenOf = new Map<string, QueueHolder[]>();
  const roots: QueueHolder[] = [];
  for (const h of holders) {
    const parent = h.parent_participant_id || h.parent;
    if (parent && byRun.has(parent)) {
      const list = childrenOf.get(parent) ?? [];
      list.push(h);
      childrenOf.set(parent, list);
    } else {
      roots.push(h);
    }
  }
  return roots.map((holder) => ({
    holder,
    children: childrenOf.get(queueRowID(holder)) ?? [],
  }));
}

export function queueLifecycleHolders(
  holders: QueueHolder[],
  waiters: QueueWaiter[],
): QueueHolder[] {
  const waitingOwners = new Set(
    waiters.filter((w) => !!w.participant_id).map((w) => w.run_id),
  );
  return holders.filter((h) => {
    const orchestrationWait =
      h.admission_waiting ||
      (!h.participant_id &&
        !h.parent &&
        (h.resources?.cores ?? 0) <= 0 &&
        (h.resources?.memory_bytes ?? 0) <= 0 &&
        waitingOwners.has(h.run_id));
    return !orchestrationWait;
  });
}

export function daemonUptimeLabel(qs: QueueState): string {
  const up = qs.daemon_uptime_ms ?? 0;
  if (up <= 0) return "";
  if (up < 1000) return "just started";
  return `up ${fmtDuration(up)}`;
}

export function hasDaemon(qs: QueueState): boolean {
  if (qs.daemon_version) return true;
  if ((qs.daemon_uptime_ms ?? 0) > 0) return true;
  if ((qs.holders?.length ?? 0) > 0) return true;
  if ((qs.waiters?.length ?? 0) > 0) return true;
  if ((qs.resources?.length ?? 0) > 0) return true;
  return false;
}

function totalEvictions(events: QueueEvents): number {
  let n = 0;
  for (const e of events.evictions ?? []) n += e.count;
  return n;
}

function plural(n: number, one: string, many: string): string {
  return n === 1 ? one : many;
}

export function eventsLine(events: QueueEvents | null | undefined): string {
  if (!events) return "";
  const evictions = totalEvictions(events);
  if (
    events.runs === 0 &&
    evictions === 0 &&
    (events.queue_timeouts ?? 0) === 0 &&
    (events.cancellations ?? 0) === 0 &&
    (events.contended ?? 0) === 0
  ) {
    return "";
  }
  const hours = Math.max(1, Math.round((events.window_ms || 0) / 3_600_000));
  const parts: string[] = [
    `${events.runs} ${plural(events.runs, "run", "runs")}`,
  ];
  if (events.runs > 0) {
    const median =
      events.median_wait_ms > 0 ? fmtDuration(events.median_wait_ms) : "0s";
    parts.push(`median wait ${median}`);
  }
  if (evictions > 0) {
    const keys = (events.evictions ?? []).map((e) => e.key);
    const label =
      keys.length === 1 ? `key: ${keys[0]}` : `keys: ${keys.join(", ")}`;
    parts.push(
      `${evictions} ${plural(evictions, "eviction", "evictions")} (${label})`,
    );
  }
  if ((events.queue_timeouts ?? 0) > 0) {
    const n = events.queue_timeouts as number;
    parts.push(`${n} ${plural(n, "queue-timeout", "queue-timeouts")}`);
  }
  if ((events.cancellations ?? 0) > 0) {
    const n = events.cancellations as number;
    parts.push(`${n} ${plural(n, "cancellation", "cancellations")}`);
  }
  if ((events.contended ?? 0) > 0) {
    const n = events.contended as number;
    parts.push(`${n} contended`);
  }
  return `last ${hours}h: ${parts.join(", ")}`;
}

export function driftNotes(
  qs: QueueState,
): { runID: string; warning: string }[] {
  const notes: { runID: string; warning: string }[] = [];
  for (const h of qs.holders ?? []) {
    if (h.drift_warning)
      notes.push({ runID: h.run_id, warning: h.drift_warning });
  }
  for (const w of qs.waiters ?? []) {
    if (w.drift_warning)
      notes.push({ runID: w.run_id, warning: w.drift_warning });
  }
  return notes;
}
