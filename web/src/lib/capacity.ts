// Shaping helpers for the Capacity panel's priced table: how a charge is
// labelled, how the table sorts, and how a percentile selection is stated as
// the rank arithmetic it is. The panel exists so a human can check
// admission's numbers by eye, so these render operands a reader can add up,
// never a summary that hides one. The host-headroom arithmetic lives beside
// the other resource-row helpers in ./queue.

import type { CapacityProfile, CapacityRankSelection } from "./api";

// CHARGE_BASIS_LABELS names the stored figure a core charge was taken from,
// in the words the docs and CLI use for it.
const CHARGE_BASIS_LABELS: Record<string, string> = {
  sustained_p95: "sustained p95",
  peak_p95: "peak p95 (profile predates sustained figures)",
  pin: "explicit pin",
  floor: "demand floor of contended runs",
  prev_charge: "previous version's charge",
  cold_start: "cold-start default",
};

export function chargeBasisLabel(basis: string): string {
  return CHARGE_BASIS_LABELS[basis] || basis;
}

// SORT_KEYS are the columns the priced table sorts by. Charge is the
// default: the panel's first question is which pipeline is expensive.
export type CapacitySortKey =
  "pipeline" | "charge" | "memory" | "samples" | "duration";

export function sortProfiles(
  profiles: CapacityProfile[],
  key: CapacitySortKey,
  ascending: boolean,
): CapacityProfile[] {
  const dir = ascending ? 1 : -1;
  const value = (p: CapacityProfile): number => {
    switch (key) {
      case "charge":
        return p.charge.cores;
      case "memory":
        return p.charge.memory_bytes;
      case "samples":
        return p.sample_count;
      case "duration":
        return p.p50_duration_ms;
      default:
        return 0;
    }
  };
  return [...profiles].sort((a, b) => {
    if (key === "pipeline") return dir * a.pipeline.localeCompare(b.pipeline);
    const delta = value(a) - value(b);
    // Ties fall back to the name so a re-render cannot reshuffle equal rows.
    return delta !== 0 ? dir * delta : a.pipeline.localeCompare(b.pipeline);
  });
}

// rankLabel states a percentile selection as the arithmetic it is: which
// position in the sorted window the charge was read from.
export function rankLabel(sel: CapacityRankSelection): string {
  if (sel.unmeasured || sel.count === 0) return "no samples";
  const pct = Math.round(sel.percentile * 100);
  return `p${pct} = rank ${sel.rank} of ${sel.count}`;
}

// mismatchNote is the warning for a stored charge its own window no longer
// reproduces -- the bug this panel exists to catch. Empty when they agree.
export function mismatchNote(
  sel: CapacityRankSelection,
  label: string,
): string {
  if (sel.matches || sel.unmeasured || sel.count === 0) return "";
  return `Stored ${label} is ${sel.stored}, but the window ranks ${sel.value} at ${rankLabel(sel)}. The charge and its samples disagree.`;
}

// pinDriftFlag is the short badge text for a pinned pipeline: whether the
// pin still matches what measurement would charge.
export function pinDriftFlag(p: CapacityProfile): string {
  const pinned = (p.pinned_cores ?? 0) > 0 || (p.pinned_memory_bytes ?? 0) > 0;
  if (!pinned) return "";
  if (!p.drift_class) return "pinned";
  return p.drift_class === "under_pinned" ? "pin low" : "pin high";
}
