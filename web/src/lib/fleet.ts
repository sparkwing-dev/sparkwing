import type { Agent } from "./api";

export type FleetRegistration = "registered" | "legacy";
export type FleetHeadroomState = "reported" | "stale" | "not-reported";

const statusOrder: Record<string, number> = { busy: 0, idle: 1, offline: 2 };
const kindOrder: Record<string, number> = {
  agent: 0,
  gateway: 1,
  pool: 2,
  local: 3,
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

export function fleetRegistration(agent: Agent): FleetRegistration {
  return agent.max_concurrent > 0 ? "registered" : "legacy";
}

export function fleetHeadroomState(agent: Agent): FleetHeadroomState {
  if (agent.headroom) return "reported";
  if (agent.status === "offline") return "stale";
  return "not-reported";
}

export function formatFleetResources(
  resources?: { cores: number; memory_bytes: number },
  zeroIsUncapped = false,
): string {
  if (!resources) return "unknown";
  const cores =
    resources.cores === 0 && zeroIsUncapped
      ? "uncapped"
      : `${resources.cores} cores`;
  const memory =
    resources.memory_bytes === 0 && zeroIsUncapped
      ? "uncapped"
      : `${(resources.memory_bytes / 1024 ** 3).toFixed(1)} GiB`;
  return `${cores} / ${memory}`;
}

export function fleetSlots(agent: Agent): string {
  const active = agent.active_slots ?? "-";
  return `${active}/${agent.max_concurrent > 0 ? agent.max_concurrent : "-"}`;
}

export function fleetSlotTotals(agents: Agent[]): {
  active: number | null;
  capacity: number | null;
} {
  let active = 0;
  let capacity = 0;
  let activeKnown = true;
  let capacityKnown = true;
  for (const agent of agents) {
    if (agent.active_slots == null) activeKnown = false;
    else active += agent.active_slots;
    if (agent.max_concurrent <= 0) capacityKnown = false;
    else capacity += agent.max_concurrent;
  }
  return {
    active: activeKnown ? active : null,
    capacity: capacityKnown ? capacity : null,
  };
}
