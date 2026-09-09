import type { CronHealth, CronLock, CronSchedule } from "./api";

export type CronTone = "ok" | "warning" | "danger" | "muted";

export interface CronHealthBanner {
  tone: "ok" | "warning" | "danger";
  headline: string;
  remedy: string;
}

const INSTALL_REMEDY =
  "Run `sparkwing crons install` in the repo that declares the schedule.";

// Seven characters is what git itself abbreviates to, so a ref reads the same
// here as in `git log --oneline`.
const SHORT_REF_LEN = 7;

/** shortRef abbreviates a locked commit the way git does. */
export function shortRef(ref: string | null | undefined): string {
  if (!ref) return "";
  return ref.length <= SHORT_REF_LEN ? ref : ref.slice(0, SHORT_REF_LEN);
}

/** fmtArgs renders a launch's arguments as the flags it passes, key order stable. */
export function fmtArgs(args: Record<string, string> | null | undefined): string {
  const keys = Object.keys(args ?? {}).sort();
  if (keys.length === 0) return "-";
  return keys.map((k) => `--${k}=${args![k]}`).join(" ");
}

export interface CronLockBadge {
  tone: CronTone;
  label: string;
}

/**
 * lockBadge names what a row runs at its next fire. A pin carries its commit
 * in the label, because the commit is the answer to "what will run"; the
 * drifted states lead with the drift, which is what the reader has to decide
 * about.
 */
export function lockBadge(lock: CronLock | null | undefined): CronLockBadge {
  const state = lock?.state ?? "follows";
  const ref = shortRef(lock?.ref);
  switch (state) {
    case "pinned":
      return { tone: "ok", label: ref ? `pinned ${ref}` : "pinned" };
    case "ahead":
      return { tone: "warning", label: "ahead" };
    case "dirty":
      return { tone: "warning", label: "dirty" };
    case "missing":
      return { tone: "danger", label: "missing" };
    default:
      return { tone: "muted", label: "follows" };
  }
}

/** lockNote is the sentence a reader needs about the lock, and what to run when it wants attention. */
export function lockNote(lock: CronLock | null | undefined): string {
  const pinRemedy =
    "Re-run `sparkwing crons install` in the repo to pin the checkout as it stands.";
  switch (lock?.state) {
    case "pinned":
      return "Every fire runs the pinned binary; the checkout is on that commit and clean.";
    case "ahead":
      return `The checkout has newer commits the pin does not carry. ${pinRemedy}`;
    case "dirty":
      return `The checkout has uncommitted edits the pin does not carry. ${pinRemedy}`;
    case "missing":
      return `The pinned binary is gone, so this schedule fires nothing. ${pinRemedy}`;
    default:
      return "No pin: every fire compiles the checkout as it stands that minute.";
  }
}

export type CronField = "cron" | "tz" | "overlap" | "catch_up" | "args";

export interface CronOverrideMarker {
  tone: CronTone;
  label: string;
  title: string;
}

/** isOverridden reports whether this host laid its own value over one declared field. */
export function isOverridden(s: CronSchedule, field: CronField): boolean {
  return (s.override?.fields ?? []).includes(field);
}

/**
 * overrideMarker describes the pill an overridden row wears, and returns null
 * for a row running what its repo declares. A stale override still applies, so
 * it is a warning rather than a fault.
 */
export function overrideMarker(
  s: CronSchedule,
): CronOverrideMarker | null {
  const fields = s.override?.fields ?? [];
  if (fields.length === 0) return null;
  const named = fields.join(", ");
  return {
    tone: s.override?.stale ? "warning" : "muted",
    label: "override",
    title: s.override?.stale
      ? `This host overrides ${named}, set against a declaration that has changed since.`
      : `This host overrides ${named}.`,
  };
}

export interface CronDiffRow {
  field: CronField;
  label: string;
  declared: string;
  effective: string;
  overridden: boolean;
}

/**
 * declaredVsEffective pairs what the repo declares with what this host runs,
 * one row per overridable field, so a reader can see which value a fire will
 * use and where it came from.
 */
export function declaredVsEffective(s: CronSchedule): CronDiffRow[] {
  const eff = s.effective;
  const row = (
    field: CronField,
    label: string,
    declared: string,
    effective: string,
  ): CronDiffRow => ({
    field,
    label,
    declared,
    effective,
    overridden: isOverridden(s, field),
  });
  return [
    row("cron", "cron", s.cron || "-", eff?.cron || "-"),
    row("tz", "tz", s.tz || "-", eff?.tz || "-"),
    row("overlap", "overlap", fmtOverlap(s.overlap), fmtOverlap(eff?.overlap ?? "")),
    row(
      "catch_up",
      "catch-up",
      fmtCatchUp(s.catch_up_ns),
      fmtCatchUp(eff?.catch_up_ns ?? 0),
    ),
    row("args", "args", fmtArgs(s.args), fmtArgs(eff?.args)),
  ];
}

// safety: the banner has room for one name and a count, so a long list is cut
// rather than pushed off the card.
const STALE_NAMES_SHOWN = 2;

function staleOverrideNames(rows: CronSchedule[]): string {
  const names = rows.filter((s) => s.override?.stale).map((s) => s.name);
  if (names.length === 0) return "";
  if (names.length <= STALE_NAMES_SHOWN) return names.join(" and ");
  const shown = names.slice(0, STALE_NAMES_SHOWN).join(", ");
  return `${shown} and ${names.length - STALE_NAMES_SHOWN} more`;
}

/**
 * healthBanner turns the crons health block into the one sentence a reader
 * needs and the command that fixes it. The first true fault wins: a missing
 * timer hides every downstream symptom, so reporting the stale tick it causes
 * would send the reader after the wrong thing.
 */
export function healthBanner(
  health: CronHealth | null | undefined,
  schedules: CronSchedule[] = [],
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
  // The pin faults come after the timer ones for the same reason: a host that
  // never ticks fires nothing, pinned or not. Among themselves a gone binary
  // outranks a stale pin, because it fires nothing at all.
  const remedy = health.remedy || INSTALL_REMEDY;
  const missing = health.missing_binary ?? 0;
  if (missing > 0) {
    return {
      tone: "warning",
      headline:
        missing === 1
          ? "A pinned pipeline binary is gone, so that schedule fires nothing."
          : `${missing} pinned pipeline binaries are gone, so those schedules fire nothing.`,
      remedy,
    };
  }
  const ahead = health.ahead ?? 0;
  if (ahead > 0) {
    return {
      tone: "warning",
      headline:
        ahead === 1
          ? "The checkout has moved past a pin, so that schedule still runs the pipeline it was pinned to."
          : `The checkout has moved past ${ahead} pins, so those schedules still run the pipelines they were pinned to.`,
      remedy,
    };
  }
  const stale = health.stale_override ?? 0;
  if (stale > 0) {
    const named = staleOverrideNames(schedules);
    return {
      tone: "warning",
      headline: named
        ? `The override on ${named} was set against a declaration that has changed since.`
        : `${stale} override${stale === 1 ? " was" : "s were"} set against a declaration that has changed since.`,
      remedy,
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
  if (health.locked > 0) parts.push(`${health.locked} locked`);
  if (health.following > 0) parts.push(`${health.following} following`);
  if (health.ahead > 0) parts.push(`${health.ahead} ahead`);
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
