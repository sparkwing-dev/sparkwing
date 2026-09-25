import type { ExecutionAttempt, Node } from "./api";

export type ExecutionLocation = "local" | "cloud" | "unknown";

export interface ExecutionDisplay {
  location: ExecutionLocation;
  locationLabel: string;
  executorLabel: string;
  platformLabel: string | null;
  className: string;
  icon: "machine" | "cloud" | "github" | "cluster" | null;
  tooltip: string | null;
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
        execution_site: node.execution_site,
        execution_site_name: node.execution_site_name,
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

// A node nobody expressed a preference for has no placement story, and an
// unclaimed one has no runner to name yet.
export function placementLabel(node: Node): string | null {
  const holder = node.claimed_by?.trim();
  if (!holder) return null;
  switch (node.placement_reason) {
    case "preference":
      return `${holder} (preferred)`;
    case "fallback":
      return `${holder} (fallback after the local-first hold)`;
    default:
      return null;
  }
}

export function executionDisplay(attempt?: ExecutionAttempt): ExecutionDisplay {
  const location = normalizeLocation(attempt?.location, attempt?.execution_site);
  const locationLabel =
    location === "local"
      ? "Local"
      : location === "cloud"
        ? "Cloud"
        : "Location unknown";
  const executorKind = attempt?.executor_kind?.trim();
  const executorName = attempt?.executor_name?.trim();
  const kind = attempt?.execution_site?.trim() || executorKind;
  const name = attempt?.execution_site_name?.trim() || executorName;
  const displayKind = executorKind || kind;
  const displayName = executorName || name;
  const executorLabel =
    displayKind && displayName
      ? `${displayKind} ${displayName}`
      : displayName || displayKind || "Executor unknown";
  const platform = attempt?.platform?.trim();
  const platformLabel = platform || null;
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
    platformLabel,
    className,
    icon,
    tooltip,
  };
}

// A missing execution site tells the reader nothing in a compact surface.
export function compactExecutionDisplay(node: Node): ExecutionDisplay | null {
  const latest = executionAttempts(node).at(-1);
  const display = executionDisplay(latest ? {
    ...latest,
    execution_site: latest.execution_site || node.execution_site,
    execution_site_name: latest.execution_site_name || node.execution_site_name,
  } : undefined);
  return display.icon ? display : null;
}

function normalizeLocation(location?: string, site?: string): ExecutionLocation {
  if (location === "local" || location === "cloud") return location;
  if (site === "machine") return "local";
  if (site === "cluster" || site === "kubernetes" || site === "github-actions" || site === "cloud") return "cloud";
  return "unknown";
}
