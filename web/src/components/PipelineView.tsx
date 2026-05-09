"use client";

import { useState, useRef, useEffect, useCallback } from "react";
import StepView from "./StepView";

export interface PipelineResult {
  pipeline: string;
  jobs: PipelineJobResult[];
  posts?: PostResult[];
  total: number;
  failed_job?: string;
}

export interface StepResult {
  name: string;
  duration: number;
  status: string;
  logs?: string;
}

export interface PipelineJobResult {
  name: string;
  duration: number;
  status: string;
  parallel?: boolean;
  rollback?: boolean;
  logs?: string;
  steps?: StepResult[];
}

interface PostResult {
  condition: string;
  name: string;
  duration: number;
}

// Group consecutive parallel jobs together for visual layout.
// Sequential jobs become single-item groups.
interface JobGroup {
  jobs: PipelineJobResult[];
  parallel: boolean;
}

function groupJobs(jobs: PipelineJobResult[]): JobGroup[] {
  const groups: JobGroup[] = [];
  let i = 0;
  while (i < jobs.length) {
    if (jobs[i].rollback) {
      // Rollback jobs get their own group
      groups.push({ jobs: [jobs[i]], parallel: false });
      i++;
    } else if (jobs[i].parallel) {
      // Collect consecutive parallel jobs
      const pJobs: PipelineJobResult[] = [];
      while (i < jobs.length && jobs[i].parallel && !jobs[i].rollback) {
        pJobs.push(jobs[i]);
        i++;
      }
      groups.push({ jobs: pJobs, parallel: true });
    } else {
      groups.push({ jobs: [jobs[i]], parallel: false });
      i++;
    }
  }
  return groups;
}

function formatDuration(ns: number): string {
  if (ns === 0) return "—";
  const s = ns / 1e9;
  if (s < 1) return `${Math.round(s * 1000)}ms`;
  return `${s.toFixed(1)}s`;
}

// "adopted" nodes (cached + coalesced) share a dashed border
// so operators see at a glance which nodes took output from elsewhere.
const statusColors: Record<
  string,
  { bg: string; border: string; text: string; dot: string }
> = {
  passed: {
    bg: "bg-green-500/10",
    border: "border-green-500/30",
    text: "text-green-400",
    dot: "bg-green-400",
  },
  failed: {
    bg: "bg-red-500/10",
    border: "border-red-500/30",
    text: "text-red-400",
    dot: "bg-red-400",
  },
  skipped: {
    bg: "bg-gray-500/10",
    border: "border-gray-500/20",
    text: "text-gray-500",
    dot: "bg-gray-500",
  },
  cached: {
    bg: "bg-cyan-500/10",
    border: "border-cyan-500/30 border-dashed",
    text: "text-cyan-400",
    dot: "bg-cyan-400",
  },
  "skipped-concurrent": {
    bg: "bg-slate-500/10",
    border: "border-slate-500/30",
    text: "text-slate-400",
    dot: "bg-slate-500",
  },
  coalesced: {
    bg: "bg-violet-500/10",
    border: "border-violet-500/30 border-dashed",
    text: "text-violet-400",
    dot: "bg-violet-400",
  },
  superseded: {
    bg: "bg-amber-500/10",
    border: "border-amber-500/30",
    text: "text-amber-400",
    dot: "bg-amber-400",
  },
};

interface Edge {
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  color: string;
}

function edgeColor(srcStatus: string, tgtStatus: string): string {
  if (srcStatus === "passed" && tgtStatus === "passed")
    return "rgba(74,222,128,0.4)";
  if (srcStatus === "passed" && tgtStatus === "failed")
    return "rgba(248,113,113,0.4)";
  if (tgtStatus === "skipped") return "rgba(107,114,128,0.25)";
  return "var(--border)";
}

export default function PipelineView({ result }: { result: PipelineResult }) {
  const [selectedJob, setSelectedJob] = useState<PipelineJobResult | null>(
    null,
  );
  const containerRef = useRef<HTMLDivElement>(null);
  const [edges, setEdges] = useState<Edge[]>([]);
  const [svgSize, setSvgSize] = useState({ width: 0, height: 0 });

  const groups = groupJobs(result.jobs);

  const computeEdges = useCallback(() => {
    const container = containerRef.current;
    if (!container) return;
    const rect = container.getBoundingClientRect();
    setSvgSize({ width: rect.width, height: rect.height });
    const next: Edge[] = [];

    for (let i = 0; i < groups.length - 1; i++) {
      const srcNodes = container.querySelectorAll<HTMLElement>(
        `[data-group="${i}"]`,
      );
      const tgtNodes = container.querySelectorAll<HTMLElement>(
        `[data-group="${i + 1}"]`,
      );
      // Use the worst status from each group for edge color
      const srcStatus = groups[i].jobs.some((j) => j.status === "failed")
        ? "failed"
        : groups[i].jobs[0]?.status || "skipped";
      const tgtStatus = groups[i + 1].jobs.some((j) => j.status === "failed")
        ? "failed"
        : groups[i + 1].jobs[0]?.status || "skipped";
      const color = edgeColor(srcStatus, tgtStatus);

      srcNodes.forEach((src) => {
        const sr = src.getBoundingClientRect();
        tgtNodes.forEach((tgt) => {
          const tr = tgt.getBoundingClientRect();
          next.push({
            x1: sr.right - rect.left,
            y1: sr.top + sr.height / 2 - rect.top,
            x2: tr.left - rect.left,
            y2: tr.top + tr.height / 2 - rect.top,
            color,
          });
        });
      });
    }
    setEdges(next);
  }, [groups]);

  useEffect(() => {
    const id = requestAnimationFrame(computeEdges);
    window.addEventListener("resize", computeEdges);
    return () => {
      cancelAnimationFrame(id);
      window.removeEventListener("resize", computeEdges);
    };
  }, [computeEdges]);

  return (
    <div>
      {/* DAG */}
      <div
        ref={containerRef}
        className="relative flex items-start gap-12 overflow-x-auto pb-4 mb-4"
      >
        <svg
          className="absolute top-0 left-0 pointer-events-none"
          width={svgSize.width}
          height={svgSize.height}
          style={{ overflow: "visible" }}
        >
          {edges.map((e, i) => {
            const dx = e.x2 - e.x1;
            return (
              <path
                key={i}
                d={`M ${e.x1} ${e.y1} C ${e.x1 + dx * 0.4} ${e.y1}, ${e.x2 - dx * 0.4} ${e.y2}, ${e.x2} ${e.y2}`}
                fill="none"
                stroke={e.color}
                strokeWidth="1.5"
              />
            );
          })}
        </svg>

        {/* Job groups as columns */}
        {groups.map((group, groupIdx) => (
          <div
            key={groupIdx}
            className="flex flex-col items-center gap-2 shrink-0"
          >
            {/* Group label for parallel groups */}
            {group.parallel && (
              <div className="text-[10px] text-[var(--muted)] uppercase tracking-wider mb-0.5">
                parallel
              </div>
            )}
            {group.jobs.map((job) => {
              const jc = statusColors[job.status] || statusColors.skipped;
              const isSelected = selectedJob?.name === job.name;
              return (
                <button
                  key={job.name}
                  data-group={groupIdx}
                  onClick={() => setSelectedJob(isSelected ? null : job)}
                  className={`relative flex items-center gap-2 px-3 py-2 rounded-lg border text-xs min-w-[150px] transition-all ${
                    isSelected
                      ? "bg-[var(--accent)]/15 border-[var(--accent)]/50 shadow-md shadow-[var(--accent)]/10"
                      : `${jc.bg} ${jc.border} hover:brightness-125`
                  }`}
                >
                  <div className={`w-2 h-2 rounded-full shrink-0 ${jc.dot}`} />
                  <span className="font-mono truncate">
                    {job.rollback ? `↩ ${job.name}` : job.name}
                  </span>
                  <span className="text-[var(--muted)] ml-auto shrink-0">
                    {formatDuration(job.duration)}
                  </span>
                </button>
              );
            })}
          </div>
        ))}
      </div>

      {/* Post handlers */}
      {result.posts && result.posts.length > 0 && (
        <div className="flex items-center gap-2 mb-4 text-xs text-[var(--muted)]">
          <span>post:</span>
          {result.posts.map((p, i) => (
            <span
              key={i}
              className="bg-[var(--background)] px-2 py-0.5 rounded font-mono"
            >
              {p.condition}: {p.name} ({formatDuration(p.duration)})
            </span>
          ))}
        </div>
      )}

      {/* Summary bar */}
      <div className="flex items-center gap-4 text-xs text-[var(--muted)] mb-4">
        <span>
          Total:{" "}
          <span className="text-[var(--foreground)] font-mono">
            {formatDuration(result.total)}
          </span>
        </span>
        {result.failed_job && (
          <span className="text-red-400">
            Failed at: <span className="font-mono">{result.failed_job}</span>
          </span>
        )}
      </div>

      {/* Selected job details */}
      {selectedJob && (
        <div>
          <div className="flex items-center gap-2 mb-2">
            <span className="text-sm font-medium">{selectedJob.name}</span>
            <span
              className={`text-xs px-1.5 py-0.5 rounded font-mono ${
                selectedJob.status === "passed"
                  ? "bg-green-500/15 text-green-400"
                  : "bg-red-500/15 text-red-400"
              }`}
            >
              {selectedJob.status}
            </span>
            <span className="text-xs text-[var(--muted)]">
              {formatDuration(selectedJob.duration)}
            </span>
          </div>
          {selectedJob.steps && selectedJob.steps.length > 0 ? (
            <StepView steps={selectedJob.steps} />
          ) : selectedJob.logs ? (
            <pre className="bg-[#0d1117] border border-[var(--border)] rounded-lg p-4 text-xs overflow-x-auto max-h-96 overflow-y-auto font-mono">
              {selectedJob.logs}
            </pre>
          ) : (
            <p className="text-sm text-[var(--muted)]">
              No logs captured for this job.
            </p>
          )}
        </div>
      )}
    </div>
  );
}
