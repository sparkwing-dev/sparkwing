"use client";

import { useEffect, useState } from "react";
import { type Node, type NodeMetrics, getNodeMetrics } from "@/lib/api";
import {
  startSerialPolling,
  summarizeNodeResources,
} from "@/lib/resourceObservability";
import {
  ResponsiveContainer,
  AreaChart,
  Area,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ReferenceLine,
} from "recharts";

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes}B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)}Ki`;
  if (bytes < 1024 * 1024 * 1024)
    return `${(bytes / (1024 * 1024)).toFixed(0)}Mi`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)}Gi`;
}

function formatCPU(millis: number): string {
  if (millis < 1000) return `${millis}m`;
  return `${(millis / 1000).toFixed(1)} CPU`;
}

function fmtElapsed(ms: number): string {
  const sec = Math.round(ms / 1000);
  if (sec < 60) return `${sec}s`;
  return `${Math.floor(sec / 60)}m${sec % 60 ? `${sec % 60}s` : ""}`;
}

function formatDurationNanos(nanos: number): string {
  const seconds = nanos / 1_000_000_000;
  if (seconds < 1) return `${Math.round(seconds * 1000)}ms`;
  if (seconds < 10) return `${seconds.toFixed(2)}s`;
  return `${seconds.toFixed(1)}s`;
}

const tooltipStyle = {
  contentStyle: {
    background: "var(--chart-tooltip)",
    border: "1px solid var(--border)",
    borderRadius: "var(--radius-control)",
    fontSize: 11,
  },
  labelStyle: { color: "var(--muted)" },
};

export default function ResourceChart({
  runID,
  node,
  isRunning,
}: {
  runID: string;
  node: Node;
  isRunning?: boolean;
}) {
  const identity = `${runID}\n${node.id}`;
  const cacheHit = node.outcome === "cached";
  const [snapshot, setSnapshot] = useState<{
    identity: string;
    metrics: NodeMetrics;
  } | null>(null);
  const metrics = snapshot?.identity === identity ? snapshot.metrics : null;

  useEffect(() => {
    if (cacheHit) return;
    return startSerialPolling({
      load: () => getNodeMetrics(runID, node.id),
      publish: (next) => setSnapshot({ identity, metrics: next }),
      intervalMS: isRunning ? 5_000 : null,
    });
  }, [cacheHit, identity, isRunning, node.id, runID]);

  if (cacheHit) {
    return (
      <div className="rounded-[var(--radius-control)] border border-[var(--chart-cache)] bg-[var(--surface-raised)] px-3 py-2 text-xs text-[var(--chart-cache)]">
        Cache hit. Sparkwing reused this node&apos;s prior result, so the job
        body did not execute and consumed no new job resources.
      </div>
    );
  }

  if (!metrics) {
    return (
      <div className="text-xs text-[var(--muted)]">
        Loading resource evidence…
      </div>
    );
  }

  const summary = summarizeNodeResources(node, metrics);
  const samplerPoints = metrics.points.filter(
    (point) => (point.cpu_time_nanos ?? 0) <= 0,
  );
  const startTime = samplerPoints[0]
    ? new Date(samplerPoints[0].ts).getTime()
    : 0;
  const data = samplerPoints.map((point) => ({
    elapsed: new Date(point.ts).getTime() - startTime,
    cpu: point.cpu_millicores,
    mem: point.memory_bytes,
  }));
  const hasSummary =
    summary.requestedCPUMillicores > 0 ||
    summary.requestedMemoryBytes > 0 ||
    summary.exactCPUTimeNanos > 0 ||
    summary.exactMaxRSSBytes > 0 ||
    summary.commandCPUTimeNanos > 0 ||
    summary.samplerCount > 0 ||
    summary.commandCount > 0;

  if (!hasSummary) {
    return (
      <div className="text-xs text-[var(--muted)]">
        {isRunning
          ? "Waiting for the first resource reading."
          : "No resource evidence was recorded for this node."}
      </div>
    );
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[10px] text-[var(--muted)]">
        <span className="font-bold uppercase tracking-wider text-[var(--foreground)]">
          Resource evidence
        </span>
        {summary.requestedCPUMillicores > 0 && (
          <span>
            Reserved CPU:{" "}
            <span className="font-mono text-[var(--chart-cpu-reserved)]">
              {formatCPU(summary.requestedCPUMillicores)}
            </span>
          </span>
        )}
        {summary.sampledPeakCPUMillicores > 0 && (
          <span>
            Interval peak CPU:{" "}
            <span className="font-mono text-[var(--chart-cpu)]">
              {formatCPU(summary.sampledPeakCPUMillicores)}
            </span>
          </span>
        )}
        {summary.commandPeakCPUMillicores > 0 && (
          <span>
            Command peak average CPU:{" "}
            <span className="font-mono text-[var(--chart-cpu)]">
              {formatCPU(summary.commandPeakCPUMillicores)}
            </span>
          </span>
        )}
        {summary.exactMeanCPUMillicores > 0 && (
          <span>
            Process mean CPU:{" "}
            <span className="font-mono text-[var(--chart-cpu)]">
              {formatCPU(summary.exactMeanCPUMillicores)}
            </span>
          </span>
        )}
        {summary.exactCPUTimeNanos > 0 && (
          <span>
            Process CPU time:{" "}
            <span className="font-mono text-[var(--chart-cpu)]">
              {formatDurationNanos(summary.exactCPUTimeNanos)}
            </span>
          </span>
        )}
        {summary.commandCPUTimeNanos > 0 && (
          <span>
            Command CPU time:{" "}
            <span className="font-mono text-[var(--chart-cpu)]">
              {formatDurationNanos(summary.commandCPUTimeNanos)}
            </span>
          </span>
        )}
        {summary.requestedMemoryBytes > 0 && (
          <span>
            Reserved memory:{" "}
            <span className="font-mono text-[var(--chart-memory-reserved)]">
              {formatBytes(summary.requestedMemoryBytes)}
            </span>
          </span>
        )}
        {summary.sampledPeakMemoryBytes > 0 && (
          <span>
            Interval peak memory:{" "}
            <span className="font-mono text-[var(--chart-memory)]">
              {formatBytes(summary.sampledPeakMemoryBytes)}
            </span>
          </span>
        )}
        {summary.exactMaxRSSBytes > 0 && (
          <span>
            Process max RSS:{" "}
            <span className="font-mono text-[var(--chart-memory)]">
              {formatBytes(summary.exactMaxRSSBytes)}
            </span>
          </span>
        )}
        {summary.commandPeakMemoryBytes > 0 && (
          <span>
            Command max RSS:{" "}
            <span className="font-mono text-[var(--chart-memory)]">
              {formatBytes(summary.commandPeakMemoryBytes)}
            </span>
          </span>
        )}
        {summary.samplerCount > 0 && (
          <span className="font-mono">
            {summary.samplerCount} interval reading
            {summary.samplerCount === 1 ? "" : "s"}
          </span>
        )}
        {summary.commandCount > 0 && (
          <span className="font-mono">
            {summary.commandCount} command report
            {summary.commandCount === 1 ? "" : "s"}
          </span>
        )}
      </div>
      {data.length > 0 && (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <ResourceArea
            title="CPU"
            data={data}
            dataKey="cpu"
            color="var(--chart-cpu)"
            formatter={formatCPU}
            reserved={summary.requestedCPUMillicores}
            reservedColor="var(--chart-cpu-reserved)"
          />
          <ResourceArea
            title="Memory"
            data={data}
            dataKey="mem"
            color="var(--chart-memory)"
            formatter={formatBytes}
            reserved={summary.requestedMemoryBytes}
            reservedColor="var(--chart-memory-reserved)"
          />
        </div>
      )}
      {summary.samplerCount === 1 && (
        <div className="text-[10px] text-[var(--muted)]">
          One interval reading was retained. The exact process totals above
          remain usable when the node finishes inside one sampling interval.
        </div>
      )}
    </div>
  );
}

function ResourceArea({
  title,
  data,
  dataKey,
  color,
  formatter,
  reserved,
  reservedColor,
}: {
  title: string;
  data: { elapsed: number; cpu: number; mem: number }[];
  dataKey: "cpu" | "mem";
  color: string;
  formatter: (value: number) => string;
  reserved: number;
  reservedColor: string;
}) {
  return (
    <div>
      <div className="mb-1 text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
        {title}
      </div>
      <div className="rounded-[var(--radius-control)] border border-[var(--border)] bg-[var(--chart-surface)]">
        <ResponsiveContainer width="100%" height={120}>
          <AreaChart data={data}>
            <CartesianGrid
              strokeDasharray="3 3"
              stroke="var(--chart-grid)"
            />
            <XAxis
              dataKey="elapsed"
              tickFormatter={fmtElapsed}
              tick={{ fontSize: 9, fill: "var(--chart-tick)" }}
            />
            <YAxis
              tickFormatter={formatter}
              tick={{ fontSize: 9, fill: "var(--chart-tick)" }}
              width={40}
            />
            <Tooltip
              {...tooltipStyle}
              labelFormatter={(value) => fmtElapsed(value as number)}
              formatter={(value) => formatter(Number(value))}
            />
            {reserved > 0 && (
              <ReferenceLine
                y={reserved}
                stroke={reservedColor}
                strokeDasharray="4 3"
                label={{ value: "reserved", fill: reservedColor, fontSize: 9 }}
              />
            )}
            <Area
              type="monotone"
              dataKey={dataKey}
              stroke={color}
              fill={color}
              fillOpacity={0.15}
              strokeWidth={1.5}
              dot={{ r: 1.5, fill: color }}
            />
          </AreaChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}
