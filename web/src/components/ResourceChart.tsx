"use client";

import { useCallback, useEffect, useState } from "react";
import { type Node, type NodeMetrics, getNodeMetrics } from "@/lib/api";
import { summarizeNodeResources } from "@/lib/resourceObservability";
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
  const [metrics, setMetrics] = useState<NodeMetrics | null>(null);

  const refresh = useCallback(async () => {
    const data = await getNodeMetrics(runID, node.id);
    setMetrics(data);
  }, [runID, node.id]);

  useEffect(() => {
    let cancelled = false;
    queueMicrotask(() => {
      if (!cancelled) void refresh();
    });
    if (isRunning) {
      const interval = setInterval(refresh, 5_000);
      return () => {
        cancelled = true;
        clearInterval(interval);
      };
    }
    return () => {
      cancelled = true;
    };
  }, [refresh, isRunning]);

  if (!metrics) {
    return (
      <div className="text-xs text-[var(--muted)]">
        Loading resource evidence…
      </div>
    );
  }

  const summary = summarizeNodeResources(node, metrics);
  if (summary.cacheHit) {
    return (
      <div className="rounded-[var(--radius-control)] border border-[var(--chart-cache)] bg-[var(--surface-raised)] px-3 py-2 text-xs text-[var(--chart-cache)]">
        Cache hit. Sparkwing reused this node&apos;s prior result, so the job
        body did not execute and consumed no new job resources.
      </div>
    );
  }

  const startTime = metrics.points[0]
    ? new Date(metrics.points[0].ts).getTime()
    : 0;
  const data = metrics.points.map((point) => ({
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
    summary.sampleCount > 0;

  if (!hasSummary) {
    return (
      <div className="text-xs text-[var(--muted)]">
        {isRunning
          ? "Waiting for the first resource reading."
          : "No resource evidence was recorded for this node."}
      </div>
    );
  }

  const measuredCPUTime =
    summary.exactCPUTimeNanos || summary.commandCPUTimeNanos;
  const measuredMemory =
    summary.exactMaxRSSBytes || summary.sampledPeakMemoryBytes;

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
            Sample peak CPU:{" "}
            <span className="font-mono text-[var(--chart-cpu)]">
              {formatCPU(summary.sampledPeakCPUMillicores)}
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
        {measuredCPUTime > 0 && (
          <span>
            {summary.exactCPUTimeNanos > 0
              ? "Process CPU time"
              : "Command CPU time"}
            :{" "}
            <span className="font-mono text-[var(--chart-cpu)]">
              {formatDurationNanos(measuredCPUTime)}
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
        {measuredMemory > 0 && (
          <span>
            {summary.exactMaxRSSBytes > 0
              ? "Process max RSS"
              : "Sample peak memory"}
            :{" "}
            <span className="font-mono text-[var(--chart-memory)]">
              {formatBytes(measuredMemory)}
            </span>
          </span>
        )}
        {summary.sampleCount > 0 && (
          <span className="font-mono">
            {summary.sampleCount} reading{summary.sampleCount === 1 ? "" : "s"}
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
      {summary.sampleCount === 1 && (
        <div className="text-[10px] text-[var(--muted)]">
          One reading was retained; the exact process totals above remain usable
          even when the node finishes inside one sampling interval.
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
