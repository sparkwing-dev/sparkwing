// Formatting + shaping helpers for the local admission queue panel.
// These mirror the CLI's `sparkwing queue` renderer (cmd/sparkwing/
// queue.go) so the dashboard and the terminal report one identical
// view: the same headroom arithmetic, the same external-pressure note,
// the same recent-events summary. Every helper tolerates absent or
// unknown fields so an older daemon that predates a field still renders.

import type {
  HostResources,
  QueueEvents,
  QueueHolder,
  QueueResource,
  QueueState,
} from "./api";

export function isHostResource(key: string): boolean {
  return key === "cores" || key === "memory";
}

// trimFloat prints a number without trailing-zero noise: whole numbers
// bare, fractions to two places.
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

// fmtAmount renders a resource amount: the memory dimension as human
// bytes, every other dimension (cores, semaphore costs) as a plain
// number.
export function fmtAmount(key: string, v: number): string {
  if (key === "memory") return humanBytes(v);
  return trimFloat(v);
}

// fmtCost renders a run's host charge as "<cores> cores" plus memory
// when it is charged.
export function fmtCost(r: HostResources | undefined): string {
  const cores = r?.cores ?? 0;
  let out = `${trimFloat(cores)} cores`;
  if (r?.memory_bytes && r.memory_bytes > 0) {
    out += `, ${humanBytes(r.memory_bytes)}`;
  }
  return out;
}

// fmtHolderCost renders a holder's charge, or a dash for an attached
// child, which rides its parent's lease and is charged nothing.
export function fmtHolderCost(h: QueueHolder): string {
  if (h.parent) return "-";
  return fmtCost(h.resources);
}

// fmtDuration renders a millisecond span rounded to whole seconds:
// "3s", "1m 30s", "2h 5m". Returns "-" for a non-positive span.
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

// fmtETA renders a waiter's estimated start offset: "now" when it is
// admitted immediately, a rounded span when it must wait, or "-" when no
// estimate is available (a run ahead lacks a measured duration).
export function fmtETA(ms: number | null | undefined): string {
  if (ms == null) return "-";
  if (ms <= 0) return "now";
  return fmtDuration(ms);
}

// resourceAvailable is the grantable amount to show for a resource row:
// the daemon's headroom-aware Available for the host dimensions, or plain
// capacity-minus-held for a semaphore row (and for older daemons that
// sent no Available).
export function resourceAvailable(r: QueueResource): number {
  const reserved = r.reserved ?? 0;
  const external = r.external ?? 0;
  const available = r.available ?? 0;
  if (
    isHostResource(r.key) &&
    (available > 0 || reserved > 0 || external > 0)
  ) {
    return available;
  }
  const free = r.capacity - r.held;
  return free < 0 ? 0 : free;
}

// externalPressureNote returns the callout to show when non-sparkwing
// load is the binding constraint -- a queue that looks idle (free
// capacity, waiters present) but refuses work because the machine is
// busy with other processes. Empty when external load is not the binding
// constraint.
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

// HolderGroup is a top-level holder with the attached children that ride
// its lease, for indented rendering.
export interface HolderGroup {
  holder: QueueHolder;
  children: QueueHolder[];
}

// groupHolders arranges holders into parent/child groups: each holder
// that draws its own budget, followed by the attached children pinned to
// its lease. A child whose named parent is absent (a lease that outlived
// its parent record) is promoted to its own top-level group so it is
// never dropped.
export function groupHolders(holders: QueueHolder[]): HolderGroup[] {
  const byRun = new Map<string, QueueHolder>();
  for (const h of holders) byRun.set(h.run_id, h);
  const childrenOf = new Map<string, QueueHolder[]>();
  const roots: QueueHolder[] = [];
  for (const h of holders) {
    if (h.parent && byRun.has(h.parent)) {
      const list = childrenOf.get(h.parent) ?? [];
      list.push(h);
      childrenOf.set(h.parent, list);
    } else {
      roots.push(h);
    }
  }
  return roots.map((holder) => ({
    holder,
    children: childrenOf.get(holder.run_id) ?? [],
  }));
}

// daemonUptimeLabel renders how long the serving daemon has been up:
// "just started" under a second, else a rounded span. Empty when the
// daemon reported no uptime (an older daemon, or none running).
export function daemonUptimeLabel(qs: QueueState): string {
  const up = qs.daemon_uptime_ms ?? 0;
  if (up <= 0) return "";
  if (up < 1000) return "just started";
  return `up ${fmtDuration(up)}`;
}

// hasDaemon reports whether a daemon is actually serving the queue.
// The endpoint returns a well-formed empty QueueState with 200 when no
// daemon is running, so an empty payload with no version and no rows is
// the "no daemon" signal, not an error.
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

// eventsLine renders the one-line recent-outcomes summary from the
// daemon's rolling window: the grant count with median wait, then only
// the trouble categories that actually occurred. Empty when the daemon
// sent no window or nothing happened in it.
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

// driftNotes collects the pin-drift warnings across holders and waiters
// so the panel surfaces each run's exact fix once.
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
