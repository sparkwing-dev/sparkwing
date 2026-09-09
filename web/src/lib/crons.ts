import type { CronHealth, CronSchedule } from "./api";

export type CronTone = "ok" | "warning" | "danger" | "muted";

export interface CronHealthBanner {
  tone: "ok" | "warning" | "danger";
  headline: string;
  remedy: string;
}

const INSTALL_REMEDY =
  "Run `sparkwing crons install` in the repo that declares the schedule.";

/**
 * healthBanner turns the crons health block into the one sentence a reader
 * needs and the command that fixes it. The first true fault wins: a missing
 * timer hides every downstream symptom, so reporting the stale tick it causes
 * would send the reader after the wrong thing.
 */
export function healthBanner(
  health: CronHealth | null | undefined,
): CronHealthBanner {
  if (!health) {
    return {
      tone: "danger",
      headline: "Cron health is unavailable.",
      remedy: "Check that the dashboard can still reach the runs store.",
    };
  }
  const timer = health.timer;
  const armed = health.armed ?? 0;
  // The server reports a foreign unit as installed: false, so this has to be
  // read before the missing-timer branch or a stranger's unit reads as absent.
  if (timer?.foreign) {
    return {
      tone: "warning",
      headline: "A cron timer is installed here, but sparkwing did not write it.",
      remedy: `Move or remove ${timer.path || "the unit"}, then re-run \`sparkwing crons install\`.`,
    };
  }
  if (!timer || !timer.installed) {
    return {
      tone: armed > 0 ? "danger" : "warning",
      headline:
        armed > 0
          ? "No cron timer is installed on this host, so nothing will fire."
          : "No cron timer is installed on this host.",
      remedy: INSTALL_REMEDY,
    };
  }
  if (timer.stale) {
    return {
      tone: "warning",
      headline: "The cron timer points at a stale sparkwing binary.",
      remedy: `Re-run \`sparkwing crons install\` to repoint it at ${timer.binary || "the current binary"}.`,
    };
  }
  if (!timer.enabled) {
    return {
      tone: "warning",
      headline: "The cron timer is installed but not enabled, so it never ticks.",
      remedy: INSTALL_REMEDY,
    };
  }
  const tick = health.last_tick;
  if (tick?.error) {
    return {
      tone: "danger",
      headline: `The last cron tick failed: ${tick.error}`,
      remedy: "Run `sparkwing crons tick` to reproduce it in the foreground.",
    };
  }
  if (!tick?.at) {
    return {
      tone: "warning",
      headline: "The cron timer has never ticked on this host.",
      remedy: "Wait a minute, or run `sparkwing crons tick` to check it by hand.",
    };
  }
  if (health.tick_stale) {
    return {
      tone: "warning",
      headline: "Cron ticks have stopped; schedules will not fire until they resume.",
      remedy: "Check the timer's log, then re-run `sparkwing crons install`.",
    };
  }
  return {
    tone: "ok",
    headline: health.detail || `${armed} schedule${armed === 1 ? "" : "s"} armed on this host.`,
    remedy: "",
  };
}

export function scheduleCounts(health: CronHealth | null | undefined): string {
  if (!health) return "";
  const parts = [`${health.schedules ?? 0} declared`, `${health.armed ?? 0} armed`];
  if (health.paused > 0) parts.push(`${health.paused} paused`);
  if (health.undeclared > 0) parts.push(`${health.undeclared} undeclared`);
  return parts.join(" · ");
}

function stateRank(s: CronSchedule): number {
  const state = s.state || (s.paused ? "paused" : s.declared ? "armed" : "undeclared");
  switch (state) {
    case "armed":
      return 0;
    case "paused":
      return 1;
    default:
      return 2;
  }
}

function dueOrder(ts: string | null | undefined): number {
  if (!ts) return Number.POSITIVE_INFINITY;
  const at = new Date(ts).getTime();
  return isNaN(at) ? Number.POSITIVE_INFINITY : at;
}

/**
 * sortSchedules orders the table by what fires next. Paused and undeclared
 * schedules sink below the armed ones whatever their stored due instant says,
 * because neither of them will actually fire.
 */
export function sortSchedules(rows: CronSchedule[]): CronSchedule[] {
  return [...rows].sort((a, b) => {
    const rank = stateRank(a) - stateRank(b);
    if (rank !== 0) return rank;
    const due = dueOrder(a.next_due_at) - dueOrder(b.next_due_at);
    if (due !== 0 && !isNaN(due)) return due;
    return a.name.localeCompare(b.name);
  });
}

export function partitionUndeclared(rows: CronSchedule[]): {
  listed: CronSchedule[];
  undeclared: CronSchedule[];
} {
  const sorted = sortSchedules(rows);
  return {
    listed: sorted.filter((s) => s.state !== "undeclared"),
    undeclared: sorted.filter((s) => s.state === "undeclared"),
  };
}

export function outcomeLabel(outcome: string): string {
  switch (outcome) {
    case "":
      return "-";
    case "skipped_overlap":
      return "skipped";
    default:
      return outcome;
  }
}

export function outcomeTone(outcome: string): CronTone {
  switch (outcome) {
    case "fired":
      return "ok";
    case "skipped_overlap":
      return "muted";
    case "missed":
      return "warning";
    case "failed":
      return "danger";
    default:
      return "muted";
  }
}

export function stateTone(state: string): CronTone {
  switch (state) {
    case "armed":
      return "ok";
    case "paused":
      return "warning";
    default:
      return "muted";
  }
}

export function fmtOverlap(overlap: string): string {
  switch (overlap) {
    case "skip":
      return "skip if running";
    case "queue":
      return "queue behind running";
    default:
      return overlap || "-";
  }
}

/** fmtCatchUp renders the store's nanosecond catch-up window as "1h" or "90m". */
export function fmtCatchUp(ns: number): string {
  const sec = Math.round((ns || 0) / 1e9);
  if (sec <= 0) return "off";
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) {
    const m = Math.floor(sec / 60);
    const s = sec % 60;
    return s ? `${m}m ${s}s` : `${m}m`;
  }
  if (sec < 86_400) {
    const h = Math.floor(sec / 3600);
    const m = Math.floor((sec % 3600) / 60);
    return m ? `${h}h ${m}m` : `${h}h`;
  }
  const d = Math.floor(sec / 86_400);
  const h = Math.floor((sec % 86_400) / 3600);
  return h ? `${d}d ${h}h` : `${d}d`;
}
