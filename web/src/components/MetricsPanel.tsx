"use client";

import { type Job, type Agent } from "@/lib/api";
import AgentUtilization from "@/components/AgentUtilization";
import { ResponsiveContainer, BarChart, Bar, Tooltip } from "recharts";

function pct(n: number, d: number): string {
  if (d === 0) return "—";
  return `${Math.round((n / d) * 100)}%`;
}

function fmtDuration(ns: number): string {
  if (!ns) return "—";
  const s = ns / 1e9;
  if (s < 1) return `${(s * 1000).toFixed(0)}ms`;
  if (s < 60) return `${s.toFixed(1)}s`;
  const m = Math.floor(s / 60);
  return `${m}m ${Math.round(s % 60)}s`;
}

function median(values: number[]): number {
  if (values.length === 0) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  const mid = Math.floor(sorted.length / 2);
  return sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2;
}

function p95(values: number[]): number {
  if (values.length === 0) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.floor(sorted.length * 0.95)];
}

// Activity sparkline — shows job volume over recent time buckets
function Sparkline({
  jobs,
  buckets = 12,
  windowHours = 24,
}: {
  jobs: Job[];
  buckets?: number;
  windowHours?: number;
}) {
  const now = Date.now();
  const windowMs = windowHours * 3600 * 1000;
  const bucketMs = windowMs / buckets;

  const data = Array.from({ length: buckets }, (_, i) => ({
    passed: 0,
    failed: 0,
    idx: i,
  }));

  for (const j of jobs) {
    const age = now - new Date(j.created_at).getTime();
    if (age > windowMs) continue;
    const idx = Math.min(buckets - 1, Math.floor((windowMs - age) / bucketMs));
    if (
      j.status === "complete" ||
      j.status === "cached" ||
      j.status === "skipped-concurrent"
    )
      data[idx].passed++;
    if (j.status === "failed" || j.status === "superseded") data[idx].failed++;
  }

  return (
    <div>
      <ResponsiveContainer width="100%" height={44}>
        <BarChart data={data} barCategoryGap={1}>
          <Tooltip
            contentStyle={{
              background: "#1a1b26",
              border: "1px solid #2a2b3a",
              borderRadius: 6,
              fontSize: 11,
            }}
            labelFormatter={() => ""}
            formatter={(v, name) => [String(v), String(name)]}
          />
          <Bar
            dataKey="passed"
            stackId="a"
            fill="rgba(74,222,128,0.5)"
            radius={[2, 2, 0, 0]}
          />
          <Bar
            dataKey="failed"
            stackId="a"
            fill="rgba(248,113,113,0.6)"
            radius={[2, 2, 0, 0]}
          />
        </BarChart>
      </ResponsiveContainer>
      <div className="flex justify-between text-[10px] text-[var(--muted)] mt-1">
        <span>{windowHours}h ago</span>
        <span>now</span>
      </div>
    </div>
  );
}

interface PipelineStats {
  name: string;
  total: number;
  passed: number;
  failed: number;
  cached: number;
  medianDuration: number;
}

function computePipelineStats(jobs: Job[]): PipelineStats[] {
  const groups: Record<string, Job[]> = {};
  for (const j of jobs) {
    if (!groups[j.pipeline]) groups[j.pipeline] = [];
    groups[j.pipeline].push(j);
  }

  return Object.entries(groups)
    .map(([name, pjobs]) => {
      const durations = pjobs
        .filter((j) => j.result?.duration)
        .map((j) => j.result!.duration);
      return {
        name,
        total: pjobs.length,
        passed: pjobs.filter((j) => j.status === "complete").length,
        failed: pjobs.filter((j) => j.status === "failed").length,
        cached: pjobs.filter((j) => j.status === "cached").length,
        medianDuration: median(durations),
      };
    })
    .sort((a, b) => b.total - a.total);
}

export default function MetricsPanel({
  jobs,
  agents,
}: {
  jobs: Job[];
  agents: Agent[];
}) {
  const topLevel = jobs.filter((j) => !j.parent_id);
  const terminal = topLevel.filter((j) =>
    [
      "complete",
      "failed",
      "cached",
      "cancelled",
      "skipped-concurrent",
      "superseded",
    ].includes(j.status),
  );
  const passed = terminal.filter(
    (j) =>
      j.status === "complete" ||
      j.status === "cached" ||
      j.status === "skipped-concurrent",
  ).length;
  const failedCount = terminal.filter(
    (j) => j.status === "failed" || j.status === "superseded",
  ).length;
  const cachedCount = topLevel.filter((j) => j.status === "cached").length;

  const durations = topLevel
    .filter((j) => j.result?.duration)
    .map((j) => j.result!.duration);
  const medDuration = median(durations);
  const p95Duration = p95(durations);

  const totalCapacity = agents.reduce((s, a) => s + a.max_concurrent, 0);
  const utilized = agents.reduce((s, a) => s + (a.active_jobs?.length || 0), 0);

  const pipelineStats = computePipelineStats(topLevel);
  const maxPipelineTotal = Math.max(1, ...pipelineStats.map((p) => p.total));

  return (
    <div className="space-y-4">
      {/* Row 1: Key rates */}
      <div className="grid grid-cols-4 gap-3">
        <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-3">
          <div className="text-xl font-bold text-green-400">
            {pct(passed, passed + failedCount)}
          </div>
          <div className="text-[10px] text-[var(--muted)]">Pass rate</div>
        </div>
        <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-3">
          <div className="text-xl font-bold font-mono">
            {fmtDuration(medDuration)}
          </div>
          <div className="text-[10px] text-[var(--muted)]">Median duration</div>
        </div>
        <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-3">
          <div className="text-xl font-bold text-cyan-400">
            {pct(cachedCount, topLevel.length)}
          </div>
          <div className="text-[10px] text-[var(--muted)]">Cache hit rate</div>
        </div>
        <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-3">
          <div className="text-xl font-bold text-indigo-400">
            {utilized}/{totalCapacity}
          </div>
          <div className="text-[10px] text-[var(--muted)]">
            Agent slots used
          </div>
        </div>
      </div>

      {/* Row 2: Activity sparkline + p95 */}
      <div className="grid grid-cols-3 gap-3">
        <div className="col-span-2 bg-[var(--surface)] border border-[var(--border)] rounded-lg p-3">
          <div className="text-[10px] text-[var(--muted)] mb-2">
            Build activity (24h)
          </div>
          <Sparkline jobs={topLevel} />
        </div>
        <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-3 flex flex-col justify-center">
          <div className="text-xs text-[var(--muted)]">p95 duration</div>
          <div className="text-xl font-bold font-mono">
            {fmtDuration(p95Duration)}
          </div>
          {durations.length > 0 && (
            <div className="text-[10px] text-[var(--muted)]">
              from {durations.length} jobs
            </div>
          )}
        </div>
      </div>

      {/* Row 3: Per-pipeline breakdown */}
      {pipelineStats.length > 0 && (
        <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-3">
          <div className="text-[10px] text-[var(--muted)] mb-2">Pipelines</div>
          <div className="space-y-2">
            {pipelineStats.slice(0, 8).map((p) => (
              <div key={p.name}>
                <div className="flex items-center justify-between mb-0.5">
                  <span className="text-xs font-mono text-violet-300 truncate">
                    {p.name}
                  </span>
                  <span className="text-[10px] text-[var(--muted)] shrink-0 ml-2">
                    <span className="text-green-400">{p.passed}</span>
                    {p.failed > 0 && (
                      <>
                        {" "}
                        / <span className="text-red-400">{p.failed}</span>
                      </>
                    )}
                    {p.cached > 0 && (
                      <>
                        {" "}
                        / <span className="text-cyan-400">{p.cached}c</span>
                      </>
                    )}
                    {p.medianDuration > 0 && (
                      <span className="ml-2">
                        {fmtDuration(p.medianDuration)}
                      </span>
                    )}
                  </span>
                </div>
                <div className="flex h-1.5 rounded-full overflow-hidden bg-[var(--background)]">
                  {p.passed > 0 && (
                    <div
                      className="bg-green-400/60"
                      style={{ width: `${(p.passed / p.total) * 100}%` }}
                    />
                  )}
                  {p.cached > 0 && (
                    <div
                      className="bg-cyan-400/60"
                      style={{ width: `${(p.cached / p.total) * 100}%` }}
                    />
                  )}
                  {p.failed > 0 && (
                    <div
                      className="bg-red-400/60"
                      style={{ width: `${(p.failed / p.total) * 100}%` }}
                    />
                  )}
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Row 4: Agent utilization timeline */}
      <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-3">
        <AgentUtilization jobs={jobs} agents={agents} />
      </div>
    </div>
  );
}
