import type { ExecutionAttempt, Node } from "./api";

export type ExecutionLocation = "local" | "cloud" | "unknown";

export interface ExecutionDisplay {
  location: ExecutionLocation;
  locationLabel: string;
  executorLabel: string;
  platformLabel: string | null;
  className: string;
}

export function executionAttempts(node: Node): ExecutionAttempt[] {
  let attempts = (node.execution_attempts ?? []).filter(
    (attempt): attempt is ExecutionAttempt =>
      typeof attempt === "object" && attempt !== null && !Array.isArray(attempt),
  );
  if (attempts.length === 0) {
    if (
      !node.executor_kind &&
      !node.executor_name &&
      !node.executor_location &&
      !node.execution_started_at
    ) {
      return [];
    }
    attempts = [
      {
        run_id: node.run_id,
        node_id: node.id,
        executor_kind: node.executor_kind,
        executor_name: node.executor_name,
        location: node.executor_location ?? "unknown",
        started_at: node.execution_started_at,
        finished_at: node.finished_at,
        outcome: node.outcome,
      },
    ];
  }
  return attempts
    .map((attempt, index) => ({
      attempt,
      index,
      ordinal: executionAttemptOrdinal(attempt),
    }))
    .sort((a, b) => {
      if (a.ordinal == null && b.ordinal == null) return a.index - b.index;
      if (a.ordinal == null) return -1;
      if (b.ordinal == null) return 1;
      return a.ordinal - b.ordinal || a.index - b.index;
    })
    .map(({ attempt }) => attempt);
}

export function executionAttemptsNewestFirst(node: Node): ExecutionAttempt[] {
  return executionAttempts(node).reverse();
}

export function executionAttemptOrdinal(
  attempt: ExecutionAttempt,
): number | null {
  const ordinal = (attempt as { attempt?: unknown }).attempt;
  return typeof ordinal === "number" &&
    Number.isSafeInteger(ordinal) &&
    ordinal > 0
    ? ordinal
    : null;
}

export function executionDisplay(attempt?: ExecutionAttempt): ExecutionDisplay {
  const location = normalizeLocation(attempt?.location);
  const locationLabel =
    location === "local"
      ? "Local"
      : location === "cloud"
        ? "Cloud"
        : "Location unknown";
  const kind = attempt?.executor_kind?.trim();
  const name = attempt?.executor_name?.trim();
  const executorLabel =
    kind && name ? `${kind} ${name}` : name || kind || "Executor unknown";
  const platform = attempt?.platform?.trim();
  const platformLabel = platform || null;
  const className =
    location === "local"
      ? "border-emerald-400/40 bg-emerald-400/10 text-emerald-200"
      : location === "cloud"
        ? "border-sky-400/40 bg-sky-400/10 text-sky-200"
        : "border-slate-400/40 bg-slate-400/10 text-slate-300";
  return {
    location,
    locationLabel,
    executorLabel,
    platformLabel,
    className,
  };
}

function normalizeLocation(location?: string): ExecutionLocation {
  if (location === "local" || location === "cloud") return location;
  return "unknown";
}
