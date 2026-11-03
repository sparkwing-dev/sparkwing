import type { NodeWorkStep } from "./api";

export type StepState =
  "succeeded" | "running" | "pending" | "failed" | "cancelled" | "skipped";

export function stepState(status: NodeWorkStep["status"]): StepState {
  switch (status) {
    case "passed":
      return "succeeded";
    case "running":
    case "failed":
    case "cancelled":
    case "skipped":
      return status;
    default:
      return "pending";
  }
}

// Running and pending share the run palette's indigo so a bar reads as
// "done so far" against "still to come"; pending is the dim half.
export const STEP_STATE_COLOR: Record<StepState, string> = {
  succeeded: "rgba(74,222,128,0.95)",
  running: "rgba(129,140,248,0.95)",
  pending: "rgba(129,140,248,0.30)",
  failed: "rgba(248,113,113,0.95)",
  cancelled: "rgba(148,163,184,0.45)",
  skipped: "rgba(148,163,184,0.45)",
};

const LABEL_ORDER: StepState[] = [
  "running",
  "failed",
  "cancelled",
  "skipped",
  "pending",
];

export function stepProgressLabel(states: StepState[]): string {
  const total = states.length;
  const counts = new Map<StepState, number>();
  for (const s of states) counts.set(s, (counts.get(s) ?? 0) + 1);
  const parts = [
    `${counts.get("succeeded") ?? 0} of ${total} step${total === 1 ? "" : "s"} succeeded`,
  ];
  for (const s of LABEL_ORDER) {
    const c = counts.get(s);
    if (c) parts.push(`${c} ${s}`);
  }
  return parts.join(", ");
}

export interface StepProgressRect {
  x: number;
  w: number;
  state: StepState;
  // Index of the first step this rect covers and how many it covers.
  first: number;
  count: number;
}

export interface StepProgressLayout {
  mode: "segments" | "proportional";
  rects: StepProgressRect[];
  // Width of one step's slot, gap included, for mapping a pointer to a step.
  slot: number;
}

// Lays out one segment per step in step order. When segments would be
// narrower than minSegment the gaps go away and runs of same-state steps
// merge into proportional fills, so the bar never outgrows its width and
// keeps step order.
export function layoutStepProgress(
  states: StepState[],
  width: number,
  gap = 1,
  minSegment = 3,
): StepProgressLayout {
  const n = states.length;
  if (n === 0 || width <= 0) return { mode: "segments", rects: [], slot: 0 };
  const segW = (width - gap * (n - 1)) / n;
  if (segW >= minSegment) {
    return {
      mode: "segments",
      slot: (width + gap) / n,
      rects: states.map((state, i) => ({
        x: i * (segW + gap),
        w: segW,
        state,
        first: i,
        count: 1,
      })),
    };
  }
  const slot = width / n;
  const rects: StepProgressRect[] = [];
  for (let i = 0; i < n; i++) {
    const last = rects[rects.length - 1];
    if (last && last.state === states[i]) {
      last.count++;
      last.w = last.count * slot;
    } else {
      rects.push({
        x: i * slot,
        w: slot,
        state: states[i],
        first: i,
        count: 1,
      });
    }
  }
  return { mode: "proportional", rects, slot };
}

export function stepIndexAt(
  layout: StepProgressLayout,
  x: number,
  n: number,
): number {
  if (n === 0 || layout.slot <= 0) return -1;
  return Math.max(0, Math.min(n - 1, Math.floor(x / layout.slot)));
}
