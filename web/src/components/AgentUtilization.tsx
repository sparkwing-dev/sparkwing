"use client";

import { type Job, type Agent } from "@/lib/api";
import {
  ResponsiveContainer,
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ReferenceLine,
  Cell,
} from "recharts";

function fmtTime(iso: string): string {
  return new Date(iso).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
  });
}

function computeUtilization(
  jobs: Job[],
  agents: Agent[],
  windowHours: number,
  buckets: number,
) {
  const now = Date.now();
  const windowMs = windowHours * 3600 * 1000;
  const bucketMs = windowMs / buckets;
  const start = now - windowMs;

  const totalCapacity = agents.reduce((s, a) => s + a.max_concurrent, 0);
  const used = new Array(buckets).fill(0);
  const timestamps = Array.from({ length: buckets }, (_, i) =>
    new Date(start + i * bucketMs).toISOString(),
  );

  for (const j of jobs) {
    if (!j.claimed_at) continue;
    const jobStart = new Date(j.claimed_at).getTime();
    const jobEnd = j.result?.duration
      ? jobStart + j.result.duration / 1e6
      : j.status === "running" || j.status === "claimed"
        ? now
        : jobStart;

    if (jobEnd < start || jobStart > now) continue;

    const startBucket = Math.max(0, Math.floor((jobStart - start) / bucketMs));
    const endBucket = Math.min(
      buckets - 1,
      Math.floor((jobEnd - start) / bucketMs),
    );

    for (let i = startBucket; i <= endBucket; i++) {
      used[i]++;
    }
  }

  return { timestamps, used, total: totalCapacity };
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

export default function AgentUtilization({
  jobs,
  agents,
  windowHours = 24,
  buckets = 48,
}: {
  jobs: Job[];
  agents: Agent[];
  windowHours?: number;
  buckets?: number;
}) {
  const { timestamps, used, total } = computeUtilization(
    jobs,
    agents,
    windowHours,
    buckets,
  );

  if (total === 0 && used.every((u) => u === 0)) return null;

  const data = timestamps.map((ts, i) => ({
    time: fmtTime(ts),
    used: used[i],
  }));

  return (
    <div>
      <div className="text-xs font-medium text-[var(--muted)] mb-2 flex items-center gap-3">
        <span>Agent Utilization</span>
        <span className="text-[10px]">
          {total} slot{total !== 1 ? "s" : ""} total
        </span>
      </div>
      <ResponsiveContainer width="100%" height={100}>
        <BarChart data={data}>
          <CartesianGrid
            strokeDasharray="3 3"
            stroke="rgba(255,255,255,0.06)"
            vertical={false}
          />
          <XAxis
            dataKey="time"
            tick={{ fontSize: 9, fill: "#666" }}
            interval={Math.floor(buckets / 4)}
          />
          <YAxis
            tick={{ fontSize: 9, fill: "#666" }}
            width={24}
            allowDecimals={false}
          />
          <Tooltip {...tooltipStyle} />
          {total > 0 && (
            <ReferenceLine
              y={total}
              stroke="rgba(248,113,113,0.4)"
              strokeDasharray="4 2"
              label={{
                value: "capacity",
                position: "right",
                fontSize: 9,
                fill: "rgba(248,113,113,0.7)",
              }}
            />
          )}
          <Bar dataKey="used" name="slots used" radius={[2, 2, 0, 0]}>
            {data.map((entry, i) => (
              <Cell
                key={i}
                fill={
                  total > 0 && entry.used >= total
                    ? "rgba(251,191,36,0.6)"
                    : "rgba(99,102,241,0.5)"
                }
              />
            ))}
          </Bar>
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}
