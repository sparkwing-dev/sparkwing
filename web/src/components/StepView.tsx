"use client";

import { useState, useCallback, useMemo } from "react";

export interface StepResult {
  name: string;
  duration: number;
  status: string;
  logs?: string;
}

function formatDuration(ns: number): string {
  if (ns === 0) return "—";
  const s = ns / 1e9;
  if (s < 1) return `${Math.round(s * 1000)}ms`;
  return `${s.toFixed(1)}s`;
}

const statusIcon: Record<string, { icon: string; color: string }> = {
  passed: { icon: "✓", color: "text-green-400" },
  failed: { icon: "✗", color: "text-red-400" },
  skipped: { icon: "–", color: "text-gray-500" },
  running: { icon: "●", color: "text-indigo-400 animate-pulse" },
};

function CopyButton({ text, label }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  const handleCopy = useCallback(() => {
    navigator.clipboard.writeText(text);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }, [text]);
  return (
    <button
      onClick={(e) => {
        e.stopPropagation();
        handleCopy();
      }}
      className="text-[10px] px-1.5 py-0.5 rounded bg-[#21262d] hover:bg-[#30363d] text-[var(--muted)] hover:text-[#c9d1d9] transition-colors"
      title={label || "Copy logs"}
    >
      {copied ? "copied" : "copy"}
    </button>
  );
}

function DownloadButton({
  text,
  filename,
}: {
  text: string;
  filename: string;
}) {
  const handleDownload = useCallback(() => {
    const blob = new Blob([text], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    a.click();
    URL.revokeObjectURL(url);
  }, [text, filename]);
  return (
    <button
      onClick={(e) => {
        e.stopPropagation();
        handleDownload();
      }}
      className="text-[10px] px-1.5 py-0.5 rounded bg-[#21262d] hover:bg-[#30363d] text-[var(--muted)] hover:text-[#c9d1d9] transition-colors"
      title="Download logs"
    >
      download
    </button>
  );
}

export default function StepView({
  steps,
  jobId,
}: {
  steps: StepResult[];
  jobId?: string;
}) {
  // Auto-expand failed steps, collapse passed ones
  const [expanded, setExpanded] = useState<Set<number>>(() => {
    const initial = new Set<number>();
    steps.forEach((s, i) => {
      if (s.status === "failed") initial.add(i);
    });
    return initial;
  });

  const toggle = (idx: number) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(idx)) next.delete(idx);
      else next.add(idx);
      return next;
    });
  };

  const allExpanded = steps.every((_, i) => expanded.has(i));
  const toggleAll = () => {
    if (allExpanded) {
      setExpanded(new Set());
    } else {
      setExpanded(new Set(steps.map((_, i) => i)));
    }
  };

  const allLogs = useMemo(
    () => steps.map((s) => s.logs || "").join("\n"),
    [steps],
  );
  const filename = jobId ? `sparkwing-${jobId}.log` : "sparkwing.log";

  return (
    <div className="bg-[#0d1117] border border-[var(--border)] rounded-lg overflow-hidden">
      {/* Header */}
      <div className="flex items-center gap-2 px-3 py-1.5 border-b border-[var(--border)] bg-[#161b22]">
        <span className="text-[10px] text-[var(--muted)] uppercase tracking-wider">
          {steps.length} steps
        </span>
        <span className="flex-1" />
        <button
          onClick={toggleAll}
          className="text-[10px] text-[var(--muted)] hover:text-[var(--foreground)] transition-colors"
        >
          {allExpanded ? "Collapse all" : "Expand all"}
        </button>
        <CopyButton text={allLogs} label="Copy all logs" />
        <DownloadButton text={allLogs} filename={filename} />
      </div>

      {/* Steps */}
      {steps.map((step, i) => {
        const isExpanded = expanded.has(i);
        const si = statusIcon[step.status] || statusIcon.skipped;
        const isFailed = step.status === "failed";

        return (
          <div
            key={i}
            className={`border-b border-[var(--border)] last:border-b-0 ${isFailed ? "bg-red-500/5" : ""}`}
          >
            {/* Step header */}
            <button
              onClick={() => toggle(i)}
              className="w-full flex items-center gap-2 px-3 py-2 text-xs hover:bg-[#1e293b]/50 transition-colors"
            >
              <span className="w-4 text-center text-[var(--muted)]">
                {isExpanded ? "▾" : "▸"}
              </span>
              <span className={`w-4 text-center ${si.color}`}>{si.icon}</span>
              <span className="font-mono text-[#c9d1d9]">{step.name}</span>
              <span className="flex items-center gap-2 ml-auto shrink-0">
                {isExpanded && step.logs && (
                  <CopyButton
                    text={step.logs}
                    label={`Copy ${step.name} logs`}
                  />
                )}
                <span className="font-mono text-[var(--muted)]">
                  {formatDuration(step.duration)}
                </span>
              </span>
            </button>

            {/* Step logs */}
            {isExpanded && (
              <div className="px-3 pb-2">
                {step.logs ? (
                  <pre className="text-xs font-mono leading-5 whitespace-pre-wrap text-[#c9d1d9] pl-8 max-h-80 overflow-y-auto">
                    {step.logs.split("\n").map((line, j) => (
                      <div key={j} className="flex hover:bg-[#161b22] group">
                        <span className="text-[#484f58] select-none pr-3 text-right shrink-0 w-8 group-hover:text-[#8b949e]">
                          {j + 1}
                        </span>
                        <span
                          className={
                            line.includes("PASS")
                              ? "text-green-400"
                              : line.includes("FAIL")
                                ? "text-red-400"
                                : line.includes("ERROR") ||
                                    line.includes("error")
                                  ? "text-red-400"
                                  : line.startsWith(">") ||
                                      line.startsWith("sparkwing:")
                                    ? "text-cyan-400"
                                    : ""
                          }
                        >
                          {line}
                        </span>
                      </div>
                    ))}
                  </pre>
                : (
                  <p className="text-xs text-[var(--muted)] pl-8">No output</p>
                )}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}
