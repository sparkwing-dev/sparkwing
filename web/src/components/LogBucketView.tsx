"use client";

import { useState, useMemo, useCallback } from "react";
import {
  type ParsedLog,
  type StepSection,
  type LogSection,
  parseLogSections,
  parseLogLines,
  hasStepBanners,
  stepNameFromSection,
} from "@/lib/logParser";
import { ansiToHtml, stripAnsi } from "@/lib/ansi";

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

const statusIcon: Record<string, { icon: string; color: string }> = {
  passed: { icon: "✓", color: "text-green-400" },
  failed: { icon: "✗", color: "text-red-400" },
  running: { icon: "●", color: "text-indigo-400 animate-pulse" },
};

function LogLines({
  lines,
  startLine,
}: {
  lines: string[];
  startLine: number;
}) {
  return (
    <pre className="text-xs font-mono leading-5 whitespace-pre-wrap text-[#c9d1d9] max-h-80 overflow-y-auto">
      {lines.map((line, j) => {
        // ANSI-colored child-process output (buildx, go test, etc.)
        // gets converted to styled spans. Lines without ANSI fall
        // through to the old semantic-keyword shading so PASS/FAIL/
        // ERROR / `> cmd` still stand out when processes don't color.
        const hasAnsi = line.includes("\x1b[");
        const stripped = hasAnsi ? stripAnsi(line) : line;
        const semantic = hasAnsi
          ? ""
          : stripped.includes("PASS")
            ? "text-green-400"
            : stripped.includes("FAIL")
              ? "text-red-400"
              : stripped.includes("ERROR") || stripped.includes("error:")
                ? "text-red-400"
                : stripped.startsWith(">") || stripped.startsWith("sparkwing:")
                  ? "text-cyan-400"
                  : stripped.match(/^(prepare|compile|cache)/)
                    ? "text-cyan-400"
                    : "";
        return (
          <div key={j} className="flex hover:bg-[#161b22] group">
            <span className="text-[#484f58] select-none pr-3 text-right shrink-0 w-8 group-hover:text-[#8b949e]">
              {startLine + j}
            </span>
            {hasAnsi ? (
              <span dangerouslySetInnerHTML={{ __html: ansiToHtml(line) }} />
            ) : (
              <span className={semantic}>{line}</span>
            )}
          </div>
        );
      })}
    </pre>
  );
}

function StepBucket({
  section,
  lineOffset,
}: {
  section: StepSection;
  lineOffset: number;
}) {
  const [expanded, setExpanded] = useState(
    section.status === "failed" || section.status === "running",
  );
  const si = statusIcon[section.status] || statusIcon.running;
  // Section.name is "<node> · <step>" for phase buckets. The node
  // half is redundant in the bucket view because the DAG selection
  // already pinned which node we're looking at, so the heading
  // shows the step name alone.
  const heading = stepNameFromSection(section);

  return (
    <div
      className={`border-b border-[var(--border)] last:border-b-0 ${section.status === "failed" ? "bg-red-500/5" : ""}`}
    >
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-2 px-3 py-2 text-xs hover:bg-[#1e293b]/50 transition-colors"
      >
        <span className="w-4 text-center text-[var(--muted)]">
          {expanded ? "▾" : "▸"}
        </span>
        <span className={`w-4 text-center ${si.color}`}>{si.icon}</span>
        <span className="font-mono text-[#c9d1d9]">{heading}</span>
        <span className="flex items-center gap-2 ml-auto shrink-0">
          {expanded && section.lines.length > 0 && (
            <CopyButton
              text={section.lines.join("\n")}
              label={`Copy ${section.name} logs`}
            />
          )}
          <span className="font-mono text-[var(--muted)]">
            {section.duration || (section.status === "running" ? "..." : "")}
          </span>
        </span>
      </button>
      {expanded && (
        <div className="px-3 pb-2">
          {section.lines.length > 0 ? (
            <div className="pl-8">
              <LogLines lines={section.lines} startLine={lineOffset} />
            </div>
          ) : (
            <p className="text-xs text-[var(--muted)] pl-8">No output</p>
          )}
        </div>
      )}
    </div>
  );
}

function BetweenSection({
  section,
  lineOffset,
}: {
  section: LogSection;
  lineOffset: number;
}) {
  const [expanded, setExpanded] = useState(false);
  const nonEmpty = section.lines.filter((l) => l.trim() !== "");
  if (nonEmpty.length === 0) return null;

  return (
    <div className="border-b border-[var(--border)] last:border-b-0">
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-2 px-3 py-1.5 text-xs hover:bg-[#1e293b]/50 transition-colors"
      >
        <span className="w-4 text-center text-[var(--muted)]">
          {expanded ? "▾" : "▸"}
        </span>
        <span className="text-[var(--muted)] italic">
          {nonEmpty.length} line{nonEmpty.length !== 1 ? "s" : ""}
        </span>
      </button>
      {expanded && (
        <div className="px-3 pb-2 pl-8">
          <LogLines lines={section.lines} startLine={lineOffset} />
        </div>
      )}
    </div>
  );
}

function SummarySection({
  section,
  lineOffset,
}: {
  section: LogSection;
  lineOffset: number;
}) {
  const [expanded, setExpanded] = useState(false);

  return (
    <div className="border-b border-[var(--border)] last:border-b-0">
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-2 px-3 py-2 text-xs hover:bg-[#1e293b]/50 transition-colors"
      >
        <span className="w-4 text-center text-[var(--muted)]">
          {expanded ? "▾" : "▸"}
        </span>
        <span className="text-cyan-400 font-mono">summary</span>
      </button>
      {expanded && (
        <div className="px-3 pb-2 pl-8">
          <LogLines lines={section.lines} startLine={lineOffset} />
        </div>
      )}
    </div>
  );
}

function InlineLogView({
  sections,
}: {
  sections: (LogSection | StepSection)[];
}) {
  // Each line in the inline view is prefixed with the step it
  // belongs to: `<step> | <line>`. That keeps a flat top-to-bottom
  // scroll attributable without the banner separators the legacy
  // STEP-banner format used. Preamble / summary / between sections
  // get no prefix because they don't belong to any step.
  const renderLine = (
    line: string,
    key: number,
    fallbackClass: string,
    stepLabel?: string,
  ) => {
    if (line.trim() === "" && !stepLabel)
      return <div key={key} className="h-5" />;
    const hasAnsi = line.includes("\x1b[");
    return (
      <div key={key} className="flex hover:bg-[#161b22] group">
        {stepLabel && (
          <span className="text-[var(--muted)] shrink-0 pr-2">
            {stepLabel}
            <span className="px-1">│</span>
          </span>
        )}
        {hasAnsi ? (
          <span dangerouslySetInnerHTML={{ __html: ansiToHtml(line) }} />
        ) : (
          <span className={fallbackClass}>{line}</span>
        )}
      </div>
    );
  };

  let lineNum = 1;
  return (
    <pre className="text-xs font-mono leading-5 whitespace-pre-wrap text-[#c9d1d9]">
      {sections.map((section, i) => {
        const startLine = lineNum;
        lineNum += section.lines.length;

        if (section.type === "step") {
          const step = section as StepSection;
          const stepLabel = stepNameFromSection(step);
          return (
            <div key={i}>
              {step.lines.map((line, j) => {
                const fallback =
                  line.includes("FAIL") || line.includes("ERROR")
                    ? "text-red-400"
                    : "";
                return renderLine(line, startLine + j, fallback, stepLabel);
              })}
            </div>
          );
        }

        if (section.type === "summary") {
          return (
            <div key={i} className="mt-2">
              {section.lines.map((line, j) => {
                const fallback = line.startsWith("✓")
                  ? "text-green-400"
                  : line.startsWith("✗")
                    ? "text-red-400"
                    : "";
                return renderLine(line, startLine + j, fallback);
              })}
            </div>
          );
        }

        // preamble / between -- no step prefix; these lines aren't
        // owned by any single step.
        return (
          <div key={i}>
            {section.lines.map((line, j) =>
              renderLine(line, startLine + j, ""),
            )}
          </div>
        );
      })}
    </pre>
  );
}

interface LogBucketViewProps {
  parsed: ParsedLog;
  jobId?: string;
}

export default function LogBucketView({ parsed, jobId }: LogBucketViewProps) {
  const [viewMode, setViewMode] = useState<"steps" | "inline">("steps");
  const steps = parsed.sections.filter(
    (s) => s.type === "step",
  ) as StepSection[];

  const allLines = useMemo(
    () => parsed.sections.flatMap((s) => s.lines),
    [parsed],
  );

  // Track cumulative line offset for line numbers
  let lineOffset = 1;

  return (
    <div className="bg-[#0d1117] border border-[var(--border)] rounded-lg overflow-hidden">
      {/* Header */}
      <div className="flex items-center gap-2 px-3 py-1.5 border-b border-[var(--border)] bg-[#161b22]">
        <span className="text-[10px] text-[var(--muted)] uppercase tracking-wider">
          {steps.length} step{steps.length !== 1 ? "s" : ""}
        </span>
        <span className="flex-1" />
        <div className="flex items-center gap-1 text-[10px]">
          <button
            onClick={() => setViewMode("steps")}
            className={`px-1.5 py-0.5 rounded transition-colors ${viewMode === "steps" ? "bg-[#30363d] text-[#c9d1d9]" : "text-[var(--muted)] hover:text-[#c9d1d9]"}`}
          >
            steps
          </button>
          <button
            onClick={() => setViewMode("inline")}
            className={`px-1.5 py-0.5 rounded transition-colors ${viewMode === "inline" ? "bg-[#30363d] text-[#c9d1d9]" : "text-[var(--muted)] hover:text-[#c9d1d9]"}`}
          >
            inline
          </button>
        </div>
        <CopyButton text={allLines.join("\n")} label="Copy all logs" />
        <DownloadButton
          text={allLines.join("\n")}
          filename={jobId ? `sparkwing-${jobId}.log` : "sparkwing.log"}
        />
      </div>

      {viewMode === "inline" ? (
        <div className="px-3 py-2">
          <InlineLogView sections={parsed.sections} />
        </div>
      ) : (
        parsed.sections.map((section, i) => {
          const offset = lineOffset;
          lineOffset += section.lines.length;

          if (section.type === "step") {
            return (
              <StepBucket
                key={i}
                section={section as StepSection}
                lineOffset={offset}
              />
            );
          }
          if (section.type === "summary") {
            return (
              <SummarySection key={i} section={section} lineOffset={offset} />
            );
          }
          if (section.type === "between" || section.type === "preamble") {
            return (
              <BetweenSection key={i} section={section} lineOffset={offset} />
            );
          }
          return null;
        })
      )}
    </div>
  );
}

// Convenience wrapper for raw log string
export function LogBucketViewFromRaw({
  rawLog,
  jobId,
}: {
  rawLog: string;
  jobId?: string;
}) {
  const parsed = useMemo(() => parseLogSections(rawLog), [rawLog]);
  return <LogBucketView parsed={parsed} jobId={jobId} />;
}

// Convenience wrapper for streaming lines
export function LogBucketViewFromLines({
  lines,
  jobId,
}: {
  lines: string[];
  jobId?: string;
}) {
  const parsed = useMemo(() => parseLogLines(lines), [lines.length]);
  return <LogBucketView parsed={parsed} jobId={jobId} />;
}

export { hasStepBanners };
