"use client";

import { useEffect, useState, useCallback } from "react";
import { type NodeMetrics, getNodeMetrics } from "@/lib/api";
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

const tooltipStyle = {
  contentStyle: {
    background: "#1a1b26",
    border: "1px solid #2a2b3a",
    borderRadius: 6,
    fontSize: 11,
  },
  labelStyle: { color: "#888" },
};

export default function ResourceChart({
  runID,
  nodeID,
  isRunning,
}: {
  runID: string;
  nodeID: string;
  isRunning?: boolean;
}) {
  const [metrics, setMetrics] = useState<NodeMetrics | null>(null);

  const refresh = useCallback(async () => {
    const data = await getNodeMetrics(runID, nodeID);
    setMetrics(data);
  }, [runID, nodeID]);

  useEffect(() => {
    refresh();
    if (isRunning) {
      const i = setInterval(refresh, 5_000);
      return () => clearInterval(i);
    }
  }, [refresh, isRunning]);

  if (!metrics || metrics.points.length < 2) return null;

  const startTime = new Date(metrics.points[0].ts).getTime();
  const data = metrics.points.map((p) => ({
    elapsed: new Date(p.ts).getTime() - startTime,
    cpu: p.cpu_millicores,
    mem: p.memory_bytes,
  }));

  const peakCPU = Math.max(...data.map((d) => d.cpu));
  const peakMem = Math.max(...data.map((d) => d.mem));
  const avgCPU = Math.round(data.reduce((s, d) => s + d.cpu, 0) / data.length);
  const avgMem = Math.round(data.reduce((s, d) => s + d.mem, 0) / data.length);

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-4 text-[10px] text-[var(--muted)]">
        <span className="font-bold uppercase tracking-wider text-[var(--foreground)]">
          Resource Usage
        </span>
        <span>
          Peak CPU:{" "}
          <span className="text-indigo-400 font-mono">
            {formatCPU(peakCPU)}
          </span>
        </span>
        <span>
          Avg CPU:{" "}
          <span className="text-indigo-400 font-mono">{formatCPU(avgCPU)}</span>
        </span>
        <span>
          Peak Mem:{" "}
          <span className="text-emerald-400 font-mono">
            {formatBytes(peakMem)}
          </span>
        </span>
        <span>
          Avg Mem:{" "}
          <span className="text-emerald-400 font-mono">
            {formatBytes(avgMem)}
          </span>
        </span>
      </div>
      <div className="grid grid-cols-2 gap-4">
        <div>
          <div className="text-[10px] text-[var(--muted)] mb-1 font-bold uppercase tracking-wider">
            CPU
          </div>
          <div className="bg-[#0d1117] rounded border border-[var(--border)]">
            <ResponsiveContainer width="100%" height={120}>
              <AreaChart data={data}>
                <CartesianGrid
                  strokeDasharray="3 3"
                  stroke="rgba(255,255,255,0.06)"
                />
                <XAxis
                  dataKey="elapsed"
                  tickFormatter={fmtElapsed}
                  tick={{ fontSize: 9, fill: "#555" }}
                />
                <YAxis
                  tickFormatter={formatCPU}
                  tick={{ fontSize: 9, fill: "#555" }}
                  width={40}
                />
                <Tooltip
                  {...tooltipStyle}
                  labelFormatter={(v) => fmtElapsed(v as number)}
                  formatter={(v) => formatCPU(Number(v))}
                />
                {metrics.cpu_limit_millicores && (
                  <ReferenceLine
                    y={metrics.cpu_limit_millicores}
                    stroke="rgba(129,140,248,0.5)"
                    strokeDasharray="4 3"
                  />
                )}
                <Area
                  type="monotone"
                  dataKey="cpu"
                  stroke="#818cf8"
                  fill="#818cf8"
                  fillOpacity={0.15}
                  strokeWidth={1.5}
                  dot={{ r: 1.5, fill: "#818cf8" }}
                />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        </div>
        <div>
          <div className="text-[10px] text-[var(--muted)] mb-1 font-bold uppercase tracking-wider">
            Memory
          </div>
          <div className="bg-[#0d1117] rounded border border-[var(--border)]">
            <ResponsiveContainer width="100%" height={120}>
              <AreaChart data={data}>
                <CartesianGrid
                  strokeDasharray="3 3"
                  stroke="rgba(255,255,255,0.06)"
                />
                <XAxis
                  dataKey="elapsed"
                  tickFormatter={fmtElapsed}
                  tick={{ fontSize: 9, fill: "#555" }}
                />
                <YAxis
                  tickFormatter={formatBytes}
                  tick={{ fontSize: 9, fill: "#555" }}
                  width={40}
                />
                <Tooltip
                  {...tooltipStyle}
                  labelFormatter={(v) => fmtElapsed(v as number)}
                  formatter={(v) => formatBytes(Number(v))}
                />
                {metrics.memory_limit_bytes && (
                  <ReferenceLine
                    y={metrics.memory_limit_bytes}
                    stroke="rgba(52,211,153,0.5)"
                    strokeDasharray="4 3"
                  />
                )}
                <Area
                  type="monotone"
                  dataKey="mem"
                  stroke="#34d399"
                  fill="#34d399"
                  fillOpacity={0.15}
                  strokeWidth={1.5}
                  dot={{ r: 1.5, fill: "#34d399" }}
                />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        </div>
      </div>
    </div>
  );
}
