import type { Node, NodeMetrics, QueueResource, QueueState } from "./api";
import { resourceAvailable } from "./queue";

export interface HostPressureDimension {
  capacity: number;
  held: number;
  reserved: number;
  external: number | null;
  available: number;
}

export interface HostPressureSample {
  at: number;
  cpu: HostPressureDimension | null;
  memory: HostPressureDimension | null;
}

export interface NodeResourceSummary {
  cacheHit: boolean;
  sampleCount: number;
  sampledPeakCPUMillicores: number;
  sampledPeakMemoryBytes: number;
  requestedCPUMillicores: number;
  requestedMemoryBytes: number;
  exactCPUTimeNanos: number;
  exactMeanCPUMillicores: number;
  exactMaxRSSBytes: number;
  commandCPUTimeNanos: number;
}

const hostPressureWindowMS = 5 * 60 * 1000;

function hostDimension(resource: QueueResource | undefined): HostPressureDimension | null {
  if (!resource) return null;
  const held = Math.max(resource.held ?? 0, 0);
  const reserved = Math.max(resource.reserved ?? 0, 0);
  const external =
    resource.external_source === "unmeasured"
      ? null
      : Math.max(resource.external ?? 0, 0);
  return {
    capacity: Math.max(resource.capacity ?? 0, 0),
    held,
    reserved,
    external,
    available: resourceAvailable(resource),
  };
}

export function hostPressureSample(
  state: QueueState,
  at: number,
): HostPressureSample | null {
  const resources = state.resources ?? [];
  const cpu = hostDimension(resources.find((resource) => resource.key === "cores"));
  const memory = hostDimension(
    resources.find((resource) => resource.key === "memory"),
  );
  if (!cpu && !memory) return null;
  return { at, cpu, memory };
}

export function appendHostPressureSample(
  samples: HostPressureSample[],
  state: QueueState,
  at: number,
  limit = 100,
): HostPressureSample[] {
  const sample = hostPressureSample(state, at);
  if (!sample) return samples;
  const next = [
    ...samples.filter((existing) => existing.at >= at - hostPressureWindowMS),
    sample,
  ];
  return next.slice(Math.max(0, next.length - Math.max(limit, 1)));
}

export function summarizeNodeResources(
  node: Node,
  metrics: NodeMetrics | null,
): NodeResourceSummary {
  const points = metrics?.points ?? [];
  const exactWall = Math.max(node.process_wall_nanos ?? 0, 0);
  const exactCPU = Math.max(node.cpu_nanos ?? 0, 0);
  return {
    cacheHit: node.outcome === "cached",
    sampleCount: points.length,
    sampledPeakCPUMillicores: Math.max(
      0,
      ...points.map((point) => point.cpu_millicores),
    ),
    sampledPeakMemoryBytes: Math.max(
      0,
      ...points.map((point) => point.memory_bytes),
    ),
    requestedCPUMillicores: Math.max(
      Math.round((node.requested_cores ?? 0) * 1000),
      0,
    ),
    requestedMemoryBytes: Math.max(node.requested_memory_bytes ?? 0, 0),
    exactCPUTimeNanos: exactCPU,
    exactMeanCPUMillicores:
      exactWall > 0 ? Math.round((exactCPU / exactWall) * 1000) : 0,
    exactMaxRSSBytes: Math.max(node.max_rss_bytes ?? 0, 0),
    commandCPUTimeNanos: points.reduce(
      (total, point) => total + Math.max(point.cpu_time_nanos ?? 0, 0),
      0,
    ),
  };
}
