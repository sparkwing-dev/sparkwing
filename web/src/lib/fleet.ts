import type { Runner } from "./api";

export type FleetHeadroomState = "reported" | "stale" | "not-reported";

const statusOrder: Record<string, number> = { busy: 0, idle: 1, offline: 2 };
export function sortFleetRunners(a: Runner, b: Runner): number {
  const statusDiff =
    (statusOrder[a.status] ?? 99) - (statusOrder[b.status] ?? 99);
  if (statusDiff !== 0) return statusDiff;
  const classDiff = a.class.localeCompare(b.class);
  if (classDiff !== 0) return classDiff;
  return a.name.localeCompare(b.name);
}

export function fleetLocation(agent: Runner): "local" | "cloud" | "unknown" {
  if (agent.location === "local" || agent.location === "cloud") {
    return agent.location;
  }
  return "unknown";
}

export function fleetHeadroomState(agent: Runner): FleetHeadroomState {
  if (agent.headroom) return "reported";
  if (agent.status === "offline") return "stale";
  return "not-reported";
}

export function formatFleetResources(resources?: {
  cores: number;
  memory_bytes: number;
}): string {
  if (!resources) return "unknown";
  return `${resources.cores} cores / ${(resources.memory_bytes / 1024 ** 3).toFixed(1)} GiB`;
}
