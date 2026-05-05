"use client";

import { useEffect, useState, useCallback } from "react";
import { type TrendPoint, getTrends } from "@/lib/api";
import {
  ResponsiveContainer,
  AreaChart,
  Area,
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  Legend,
  Line,
  ComposedChart,
} from "recharts";

function fmtMs(ms: number): string {
  if (!ms) return "0";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  const m = Math.floor(s / 60);
  return `${m}m ${Math.round(s % 60)}s`;
}

function fmtTime(iso: string): string {
  return new Date(iso).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
  });
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

export default function TrendCharts({
  pipeline,
  hours = 72,
}: {
  pipeline?: string;
  hours?: number;
}) {
  const [points, setPoints] = useState<TrendPoint[]>([]);

  const refresh = useCallback(async () => {
    const resp = await getTrends({ pipeline, hours });
    setPoints(resp.points || []);
  }, [pipeline, hours]);

  useEffect(() => {
    refresh();
    const i = setInterval(refresh, 30_000);
    return () => clearInterval(i);
  }, [refresh]);

  if (points.length < 2) {
    return (
      <div className="text-xs text-[var(--muted)] p-4">
        Not enough data for trends yet.
      </div>
    );
  }

  const data = points.map((p) => ({
    ...p,
    time: fmtTime(p.bucket),
  }));

  return (
    <div className="space-y-4">
      {/* Duration chart: avg + p95 */}
      <div>
        <div className="text-xs font-medium text-[var(--muted)] mb-2">
          Duration
        </div>
        <ResponsiveContainer width="100%" height={140}>
          <ComposedChart data={data}>
            <CartesianGrid
              strokeDasharray="3 3"
              stroke="rgba(255,255,255,0.06)"
            />
            <XAxis dataKey="time" tick={{ fontSize: 10, fill: "#666" }} />
            <YAxis
              tickFormatter={fmtMs}
              tick={{ fontSize: 10, fill: "#666" }}
              width={50}
            />
            <Tooltip {...tooltipStyle} formatter={(v) => fmtMs(Number(v))} />
            <Area
              type="monotone"
              dataKey="avg_dur_ms"
              name="avg"
              stroke="#6366f1"
              fill="#6366f1"
              fillOpacity={0.15}
              strokeWidth={1.5}
            />
            <Line
              type="monotone"
              dataKey="p95_dur_ms"
              name="p95"
              stroke="#f472b6"
              strokeWidth={1}
              strokeDasharray="4 2"
              dot={false}
            />
          </ComposedChart>
        </ResponsiveContainer>
      </div>

      {/* Queue wait chart */}
      <div>
        <div className="text-xs font-medium text-[var(--muted)] mb-2">
          Queue Wait
        </div>
        <ResponsiveContainer width="100%" height={120}>
          <AreaChart data={data}>
            <CartesianGrid
              strokeDasharray="3 3"
              stroke="rgba(255,255,255,0.06)"
            />
            <XAxis dataKey="time" tick={{ fontSize: 10, fill: "#666" }} />
            <YAxis
              tickFormatter={fmtMs}
              tick={{ fontSize: 10, fill: "#666" }}
              width={50}
            />
            <Tooltip {...tooltipStyle} formatter={(v) => fmtMs(Number(v))} />
            <Area
              type="monotone"
              dataKey="avg_wait_ms"
              name="avg wait"
              stroke="#fbbf24"
              fill="#fbbf24"
              fillOpacity={0.15}
              strokeWidth={1.5}
            />
          </AreaChart>
        </ResponsiveContainer>
      </div>

      {/* Success rate stacked bar */}
      <div>
        <div className="text-xs font-medium text-[var(--muted)] mb-2">
          Success Rate
        </div>
        <ResponsiveContainer width="100%" height={120}>
          <BarChart data={data}>
            <CartesianGrid
              strokeDasharray="3 3"
              stroke="rgba(255,255,255,0.06)"
            />
            <XAxis dataKey="time" tick={{ fontSize: 10, fill: "#666" }} />
            <YAxis tick={{ fontSize: 10, fill: "#666" }} width={30} />
            <Tooltip {...tooltipStyle} />
            <Legend iconSize={8} wrapperStyle={{ fontSize: 10 }} />
            <Bar
              dataKey="passed"
              stackId="a"
              fill="rgba(74,222,128,0.7)"
              name="pass"
            />
            <Bar
              dataKey="cached"
              stackId="a"
              fill="rgba(103,232,249,0.5)"
              name="cached"
            />
            <Bar
              dataKey="failed"
              stackId="a"
              fill="rgba(248,113,113,0.7)"
              name="fail"
            />
          </BarChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}
