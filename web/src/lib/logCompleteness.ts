// The logs service's verdict on whether one node's log is whole. The
// dashboard draws it as a synthetic line after the log: it is never part
// of the stored log, never counted, and never in a download.
export type LogCompletenessState =
  | "streaming"
  | "complete"
  | "incomplete"
  | "cut_off"
  | "unconfirmed"
  | "unknown";

export interface LogCompleteness {
  state: LogCompletenessState;
  lines: number;
  missing_lines?: number;
  message?: string;
}

// completenessLine is the text of the synthetic line, or null when the
// log needs none: it is whole, still arriving, or its store keeps no seals.
export function completenessLine(c: LogCompleteness | null): string | null {
  if (!c || !c.message) return null;
  if (
    c.state === "complete" ||
    c.state === "streaming" ||
    c.state === "unknown"
  ) {
    return null;
  }
  return `[${c.message}]`;
}

// completenessSettled reports whether the verdict can change without the
// node running again. A finished node reads "streaming" only while the
// runner's seal is still inside its grace, so the caller asks again.
export function completenessSettled(c: LogCompleteness | null): boolean {
  return c !== null && c.state !== "streaming";
}
