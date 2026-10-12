import type { Agent } from "./api";

export type FleetHeadroomState = "reported" | "stale" | "not-reported";

const statusOrder: Record<string, number> = { busy: 0, idle: 1, offline: 2 };
const kindOrder: Record<string, number> = {
  agent: 0,
  pool: 1,
  local: 2,
};

export function sortFleetAgents(a: Agent, b: Agent): number {
  const statusDiff =
    (statusOrder[a.status] ?? 99) - (statusOrder[b.status] ?? 99);
  if (statusDiff !== 0) return statusDiff;
  const kindDiff = (kindOrder[a.type] ?? 99) - (kindOrder[b.type] ?? 99);
  if (kindDiff !== 0) return kindDiff;
  return a.name.localeCompare(b.name);
}

export function fleetLocation(agent: Agent): "local" | "cloud" | "unknown" {
  if (agent.location === "local" || agent.location === "cloud") {
    return agent.location;
  }
  return "unknown";
}

export function fleetHeadroomState(agent: Agent): FleetHeadroomState {
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
