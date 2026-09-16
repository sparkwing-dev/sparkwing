import type { Node, NodeMetrics, QueueResource, QueueState } from "./api";

export interface HostPressureDimension {
  capacity: number;
  held: number;
  reserved: number;
  external: number | null;
}

export interface HostPressureSample {
  at: number;
  cpu: HostPressureDimension | null;
  memory: HostPressureDimension | null;
}

export interface NodeResourceSummary {
  samplerCount: number;
  commandCount: number;
  sampledPeakCPUMillicores: number;
  sampledPeakMemoryBytes: number;
  commandPeakCPUMillicores: number;
  commandPeakMemoryBytes: number;
  requestedCPUMillicores: number;
  requestedMemoryBytes: number;
  exactCPUTimeNanos: number;
  exactMeanCPUMillicores: number;
  exactMaxRSSBytes: number;
  commandCPUTimeNanos: number;
}

const hostPressureWindowMS = 5 * 60 * 1000;

export interface SerialPollingOptions<T> {
  load: () => Promise<T>;
  publish: (value: T) => void;
  intervalMS: number | null;
  active?: () => boolean;
}

export function startSerialPolling<T>({
  load,
  publish,
  intervalMS,
  active,
}: SerialPollingOptions<T>): () => void {
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | undefined;

  const schedule = () => {
    if (intervalMS === null || stopped) return;
    timer = setTimeout(poll, intervalMS);
  };
  const poll = async () => {
    if (stopped) return;
    if (active && !active()) {
      schedule();
      return;
    }
    const value = await load();
    if (stopped) return;
    publish(value);
    schedule();
  };

  void poll();
  return () => {
    stopped = true;
    if (timer !== undefined) clearTimeout(timer);
  };
}

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
  const samplerPoints = points.filter(
    (point) => (point.cpu_time_nanos ?? 0) <= 0,
  );
  const commandPoints = points.filter(
    (point) => (point.cpu_time_nanos ?? 0) > 0,
  );
  const exactWall = Math.max(node.process_wall_nanos ?? 0, 0);
  const exactCPU = Math.max(node.cpu_nanos ?? 0, 0);
  return {
    samplerCount: samplerPoints.length,
    commandCount: commandPoints.length,
    sampledPeakCPUMillicores: Math.max(
      0,
      ...samplerPoints.map((point) => point.cpu_millicores),
    ),
    sampledPeakMemoryBytes: Math.max(
      0,
      ...samplerPoints.map((point) => point.memory_bytes),
    ),
    commandPeakCPUMillicores: Math.max(
      0,
      ...commandPoints.map((point) => point.cpu_millicores),
    ),
    commandPeakMemoryBytes: Math.max(
      0,
      ...commandPoints.map((point) => point.memory_bytes),
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
    commandCPUTimeNanos: commandPoints.reduce(
      (total, point) => total + Math.max(point.cpu_time_nanos ?? 0, 0),
      0,
    ),
  };
}
