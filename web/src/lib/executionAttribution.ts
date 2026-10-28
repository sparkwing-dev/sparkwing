import type { ExecutionAttempt, Node } from "./api";

export type ExecutionLocation = "local" | "cloud" | "unknown";

export interface ExecutionDisplay {
  location: ExecutionLocation;
  locationLabel: string;
  executorLabel: string;
  className: string;
  icon: "machine" | "cloud" | "github" | "cluster" | null;
  tooltip: string | null;
}

export function executionAttempts(node: Node): ExecutionAttempt[] {
  return (node.execution_attempts ?? [])
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
  const className =
    location === "local"
      ? "border-emerald-400/40 bg-emerald-400/10 text-emerald-200"
      : location === "cloud"
        ? "border-sky-400/40 bg-sky-400/10 text-sky-200"
        : "border-slate-400/40 bg-slate-400/10 text-slate-300";
  let icon: ExecutionDisplay["icon"] = null;
  let tooltip: string | null = null;
  if (kind === "github-actions") {
    icon = "github";
    tooltip = `Ran on GitHub Actions${name ? `: ${name}` : ""}`;
  } else if (kind === "cluster" || kind === "kubernetes" || kind === "k8s") {
    icon = "cluster";
    tooltip = `Ran on the cluster${name ? ` ${name.replaceAll("-", " ")}` : ""}`;
  } else if (kind === "cloud" || location === "cloud") {
    icon = "cloud";
    tooltip = "Ran in Sparkwing Cloud";
  } else if (kind === "machine" || location === "local") {
    icon = "machine";
    tooltip = `Ran on ${name || "your machine"}${name ? " (your machine)" : ""}`;
  }
  return {
    location,
    locationLabel,
    executorLabel,
    className,
    icon,
    tooltip,
  };
}

// A missing execution site tells the reader nothing in a compact surface.
export function compactExecutionDisplay(node: Node): ExecutionDisplay | null {
  const display = executionDisplay(executionAttempts(node).at(-1));
  return display.icon ? display : null;
}

function normalizeLocation(location?: string): ExecutionLocation {
  return location === "local" || location === "cloud" ? location : "unknown";
}
