"use client";

import type {
  HostPressureDimension,
  HostPressureSample,
} from "@/lib/resourceObservability";
import { humanBytes, trimFloat } from "@/lib/queue";
import {
  Area,
  CartesianGrid,
  ComposedChart,
  Line,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

const tooltipStyle = {
  contentStyle: {
    background: "var(--chart-tooltip)",
    border: "1px solid var(--border)",
    borderRadius: "var(--radius-control)",
    fontSize: 11,
  },
  labelStyle: { color: "var(--muted)" },
};

function elapsedLabel(ms: number): string {
  const seconds = Math.max(Math.round(ms / 1000), 0);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m${seconds % 60 ? `${seconds % 60}s` : ""}`;
}

export default function HostPressureChart({
  samples,
  ignoreExternal,
}: {
  samples: HostPressureSample[];
  ignoreExternal?: boolean;
}) {
  const cpu = chartData(samples, "cpu");
  const memory = chartData(samples, "memory");
  if (cpu.length === 0 && memory.length === 0) return null;

  return (
    <div className="space-y-2 rounded-[var(--radius-panel)] border border-[var(--border)] bg-[var(--surface)] p-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <div>
          <div className="text-xs font-bold uppercase tracking-wider text-[var(--foreground)]">
            Live host pressure
          </div>
          <div className="text-[10px] text-[var(--muted)]">
            Sparkwing jobs are held reservations. External apps are measured
            host use outside Sparkwing.
          </div>
        </div>
        <div className="flex flex-wrap gap-3 text-[10px] text-[var(--muted)]">
          <Legend color="var(--chart-cpu)" label="Sparkwing jobs" />
          <Legend color="var(--chart-external)" label="External apps" />
          <Legend color="var(--chart-host-reserve)" label="Host reserve" />
          <Legend color="var(--chart-capacity)" label="Capacity" line />
        </div>
      </div>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {cpu.length > 0 && (
          <PressureArea
            title="CPU"
            data={cpu}
            formatter={(value) => `${trimFloat(value)} cores`}
          />
        )}
        {memory.length > 0 && (
          <PressureArea title="Memory" data={memory} formatter={humanBytes} />
        )}
      </div>
      <div className="text-[10px] text-[var(--muted)]">
        History starts when this page opens and keeps the latest five minutes.
        An external gap means the host sensor reported unmeasured, not zero.
      </div>
      {ignoreExternal && (
        <div className="text-[10px] text-[var(--warning)]">
          External load is shown, but this daemon is configured not to subtract
          it from admission capacity.
        </div>
      )}
    </div>
  );
}

function Legend({
  color,
  label,
  line,
}: {
  color: string;
  label: string;
  line?: boolean;
}) {
  return (
    <span className="inline-flex items-center gap-1">
      <span
        className={
          line ? "h-px w-3" : "h-2 w-2 rounded-[var(--radius-control)]"
        }
        style={{ background: color }}
      />
      {label}
    </span>
  );
}

interface PressurePoint {
  elapsed: number;
  capacity: number;
  held: number;
  reserved: number;
  external: number | null;
}

function chartData(
  samples: HostPressureSample[],
  key: "cpu" | "memory",
): PressurePoint[] {
  const first = samples[0]?.at ?? 0;
  return samples.flatMap((sample) => {
    const value: HostPressureDimension | null = sample[key];
    if (!value) return [];
    return [
      {
        elapsed: sample.at - first,
        capacity: value.capacity,
        held: value.held,
        reserved: value.reserved,
        external: value.external,
      },
    ];
  });
}

function PressureArea({
  title,
  data,
  formatter,
}: {
  title: string;
  data: PressurePoint[];
  formatter: (value: number) => string;
}) {
  return (
    <div>
      <div className="mb-1 text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
        {title}
      </div>
      <div className="rounded-[var(--radius-control)] border border-[var(--border)] bg-[var(--chart-surface)]">
        <ResponsiveContainer width="100%" height={180}>
          <ComposedChart data={data}>
            <CartesianGrid
              strokeDasharray="3 3"
              stroke="var(--chart-grid)"
            />
            <XAxis
              dataKey="elapsed"
              tickFormatter={elapsedLabel}
              tick={{ fontSize: 9, fill: "var(--chart-tick)" }}
            />
            <YAxis
              tickFormatter={formatter}
              tick={{ fontSize: 9, fill: "var(--chart-tick)" }}
              width={58}
            />
            <Tooltip
              {...tooltipStyle}
              labelFormatter={(value) => elapsedLabel(value as number)}
              formatter={(value) => formatter(Number(value))}
            />
            <Area
              name="Sparkwing jobs"
              type="monotone"
              dataKey="held"
              stackId="pressure"
              stroke="var(--chart-cpu)"
              fill="var(--chart-cpu)"
              fillOpacity={0.45}
              dot={false}
            />
            <Area
              name="Host reserve"
              type="monotone"
              dataKey="reserved"
              stackId="pressure"
              stroke="var(--chart-host-reserve)"
              fill="var(--chart-host-reserve)"
              fillOpacity={0.4}
              dot={false}
            />
            <Area
              name="External apps"
              type="monotone"
              dataKey="external"
              stackId="pressure"
              stroke="var(--chart-external)"
              fill="var(--chart-external)"
              fillOpacity={0.5}
              connectNulls={false}
              dot={false}
            />
            <Line
              name="Capacity"
              type="stepAfter"
              dataKey="capacity"
              stroke="var(--chart-capacity)"
              strokeDasharray="4 3"
              strokeWidth={1}
              dot={false}
            />
          </ComposedChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}
