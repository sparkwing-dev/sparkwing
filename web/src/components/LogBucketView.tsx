"use client";

import { useEffect, useState, useMemo, useCallback, useRef } from "react";
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

// TS_PREFIX_RE matches the [HH:MM:SS.mmm] prefix baked into JSON-
// derived log lines by recordToLine. The renderer splits this off so
// it can be styled and toggled independently of the line body.
const TS_PREFIX_RE = /^\[(\d{2}:\d{2}:\d{2}\.\d{3})\]\s/;

function LogLines({
  lines,
  startLine,
  showTimestamps = true,
  matchLineSet,
  currentMatchLine,
  visibleLineSet,
}: {
  lines: string[];
  startLine: number;
  showTimestamps?: boolean;
  // When set, lines whose absolute number is in the set are painted
  // with a yellow wash; the currentMatchLine gets a brighter band.
  matchLineSet?: Set<number>;
  currentMatchLine?: number;
  // When set (filter mode), lines NOT in the set are hidden; gaps
  // between visible lines collapse into a "…" separator row.
  visibleLineSet?: Set<number> | null;
}) {
  let lastVisibleIdx = -2;
  const rendered: React.ReactElement[] = [];
  lines.forEach((line, j) => {
    const absLine = startLine + j;
    if (visibleLineSet && !visibleLineSet.has(absLine)) return;
    if (visibleLineSet && lastVisibleIdx >= 0 && j !== lastVisibleIdx + 1) {
      rendered.push(
        <div
          key={`gap-${j}`}
          className="flex text-[#484f58] select-none px-3 py-0.5 text-[10px] italic"
        >
          … {j - lastVisibleIdx - 1} hidden
        </div>,
      );
    }
    lastVisibleIdx = j;
    rendered.push(
      renderLogLine(
        line,
        absLine,
        showTimestamps,
        matchLineSet,
        currentMatchLine,
      ),
    );
  });
  return (
    <pre className="text-xs font-mono leading-5 whitespace-pre-wrap text-[#c9d1d9]">
      {rendered}
    </pre>
  );
}

function renderLogLine(
  line: string,
  absLine: number,
  showTimestamps: boolean,
  matchLineSet: Set<number> | undefined,
  currentMatchLine: number | undefined,
): React.ReactElement {
  const tsMatch = line.match(TS_PREFIX_RE);
  const ts = tsMatch ? tsMatch[1] : null;
  const body = tsMatch ? line.slice(tsMatch[0].length) : line;
  const hasAnsi = body.includes("\x1b[");
  const stripped = hasAnsi ? stripAnsi(body) : body;
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
  const isMatch = matchLineSet?.has(absLine);
  const isCurrent = isMatch && currentMatchLine === absLine;
  const matchCls = isCurrent
    ? "bg-yellow-400/30"
    : isMatch
      ? "bg-yellow-400/10"
      : "";
  return (
    <div
      key={absLine}
      data-line={absLine}
      className={`flex hover:bg-[#161b22] group ${matchCls}`}
    >
      <span className="text-[#484f58] select-none pr-3 text-right shrink-0 w-8 group-hover:text-[#8b949e]">
        {absLine}
      </span>
      {showTimestamps && ts && (
        <span className="text-[#6e7681] select-none pr-2 shrink-0 tabular-nums">
          {ts}
        </span>
      )}
      {hasAnsi ? (
        <span dangerouslySetInnerHTML={{ __html: ansiToHtml(body) }} />
      ) : (
        <span className={semantic}>{body}</span>
      )}
    </div>
  );
}

function fmtOffset(ms: number): string {
  if (ms < 0) ms = 0;
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms - m * 60_000) / 1000);
  return s > 0 ? `${m}m ${s}s` : `${m}m`;
}

function StepBucket({
  section,
  lineOffset,
  maxDurationMs,
  waterfallStartMs,
  waterfallTotalMs,
  expanded: expandedProp,
  onToggle,
  showTimestamps,
  isResult,
  hasSkipIf,
  matchLineSet,
  currentMatchLine,
  visibleLineSet,
}: {
  section: StepSection;
  lineOffset: number;
  maxDurationMs: number;
  // When set, the bar renders as a positioned waterfall segment
  // relative to the chart's overall timeline. When zero/null we fall
  // back to a left-aligned, length-only bar.
  waterfallStartMs?: number | null;
  waterfallTotalMs?: number | null;
  expanded?: boolean;
  onToggle?: () => void;
  showTimestamps?: boolean;
  // Step-level attributes that mirror the StepDag pill set.
  isResult?: boolean;
  hasSkipIf?: boolean;
  matchLineSet?: Set<number>;
  currentMatchLine?: number;
  visibleLineSet?: Set<number> | null;
}) {
  const defaultExpanded =
    section.status === "failed" || section.status === "running";
  const [localExpanded, setLocalExpanded] = useState(defaultExpanded);
  const expanded = expandedProp ?? localExpanded;
  const setExpanded = (next: boolean) => {
    if (onToggle) onToggle();
    else setLocalExpanded(next);
  };
  const si = statusIcon[section.status] || statusIcon.running;
  const heading = stepNameFromSection(section);

  const useWaterfall =
    !!waterfallTotalMs &&
    waterfallTotalMs > 0 &&
    section.startedAtMs != null &&
    (section.durationMs ?? 0) > 0;
  let barLeftPct = 0;
  let barWidthPct = 0;
  if (useWaterfall) {
    const offset = (section.startedAtMs ?? 0) - (waterfallStartMs ?? 0);
    barLeftPct = Math.max(0, Math.min(100, (offset / waterfallTotalMs!) * 100));
    barWidthPct = Math.max(
      2,
      Math.min(
        100 - barLeftPct,
        ((section.durationMs ?? 0) / waterfallTotalMs!) * 100,
      ),
    );
  }
  const barPct =
    !useWaterfall && maxDurationMs > 0 && section.durationMs
      ? Math.max(2, Math.round((section.durationMs / maxDurationMs) * 100))
      : 0;
  const barColor =
    section.status === "failed"
      ? "bg-red-400/60"
      : section.status === "running"
        ? "bg-indigo-400/60"
        : "bg-cyan-400/50";

  // stepNameFromSection strips the "<nodeId> · " prefix when present;
  // leaves the bare node-scope sections (the setup bucket) returning
  // the full section.name. The data attribute below matches what
  // external panes use to drive focus-step scrolling.
  const dataStep = section.name.includes(" · ")
    ? section.name.split(" · ").slice(-1)[0]
    : null;
  return (
    <div
      data-step-id={dataStep ?? undefined}
      className={`border-b border-[var(--border)] last:border-b-0 ${section.status === "failed" ? "bg-red-500/5" : ""}`}
    >
      {/* Header is a div, not a button, so the inline CopyButton can
        sit inside it without nesting button elements (invalid HTML).
        Keyboard activation is wired via role + Enter/Space handler. */}
      <div
        role="button"
        tabIndex={0}
        onClick={() => setExpanded(!expanded)}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            setExpanded(!expanded);
          }
        }}
        className="w-full flex items-center gap-2 px-3 py-2 text-xs hover:bg-[#1e293b]/50 transition-colors cursor-pointer"
      >
        <span className="w-4 text-center text-[var(--muted)]">
          {expanded ? "▾" : "▸"}
        </span>
        <span className={`w-4 text-center ${si.color}`}>{si.icon}</span>
        <span className="font-mono text-[#c9d1d9] truncate">{heading}</span>
        {isResult && (
          <span
            className="px-1.5 rounded text-[10px] bg-green-500/15 text-green-300 shrink-0"
            title="step output is the node's result"
          >
            result
          </span>
        )}
        {hasSkipIf && (
          <span
            className="px-1.5 rounded text-[10px] bg-amber-500/15 text-amber-300 shrink-0"
            title="step has a SkipIf guard"
          >
            skipIf
          </span>
        )}
        {(section.duration || section.status === "running") && (
          <span className="font-mono text-[var(--muted)] tabular-nums text-[10px] shrink-0">
            {section.duration || "..."}
          </span>
        )}
        {section.lines.length > 0 && (
          <span
            className="font-mono text-[var(--muted)] tabular-nums text-[10px] shrink-0"
            title={`${section.lines.length} log line${section.lines.length === 1 ? "" : "s"}`}
          >
            {section.lines.length}L
          </span>
        )}
        <span className="flex items-center gap-2 ml-auto shrink-0">
          {expanded && section.lines.length > 0 && (
            <CopyButton
              text={section.lines.join("\n")}
              label={`Copy ${section.name} logs`}
            />
          )}
          {useWaterfall ? (
            <span
              className="hidden sm:inline-block w-32 h-1.5 bg-[#161b22] rounded overflow-hidden relative"
              title={`Started +${fmtOffset((section.startedAtMs ?? 0) - (waterfallStartMs ?? 0))} · ran ${section.duration || "..."}`}
            >
              <span
                className={`absolute top-0 h-full ${barColor} rounded-sm`}
                style={{
                  left: `${barLeftPct}%`,
                  width: `${barWidthPct}%`,
                }}
              />
            </span>
          ) : (
            barPct > 0 && (
              <span
                className="hidden sm:inline-block w-16 h-1.5 bg-[#161b22] rounded overflow-hidden"
                title={`Duration: ${section.duration || "..."} · proportional to longest step`}
              >
                <span
                  className={`block h-full ${barColor}`}
                  style={{ width: `${barPct}%` }}
                />
              </span>
            )
          )}
        </span>
      </div>
      {expanded && (
        <div className="px-3 pb-2">
          {section.lines.length > 0 ? (
            <div className="pl-8">
              <LogLines
                lines={section.lines}
                startLine={lineOffset}
                showTimestamps={showTimestamps}
                matchLineSet={matchLineSet}
                currentMatchLine={currentMatchLine}
                visibleLineSet={visibleLineSet}
              />
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
  showTimestamps,
  matchLineSet,
  currentMatchLine,
  visibleLineSet,
}: {
  section: LogSection;
  lineOffset: number;
  showTimestamps?: boolean;
  matchLineSet?: Set<number>;
  currentMatchLine?: number;
  visibleLineSet?: Set<number> | null;
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
          <LogLines
            lines={section.lines}
            startLine={lineOffset}
            showTimestamps={showTimestamps}
            matchLineSet={matchLineSet}
            currentMatchLine={currentMatchLine}
            visibleLineSet={visibleLineSet}
          />
        </div>
      )}
    </div>
  );
}

function SummarySection({
  section,
  lineOffset,
  showTimestamps,
  matchLineSet,
  currentMatchLine,
  visibleLineSet,
}: {
  section: LogSection;
  lineOffset: number;
  showTimestamps?: boolean;
  matchLineSet?: Set<number>;
  currentMatchLine?: number;
  visibleLineSet?: Set<number> | null;
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
          <LogLines
            lines={section.lines}
            startLine={lineOffset}
            showTimestamps={showTimestamps}
            matchLineSet={matchLineSet}
            currentMatchLine={currentMatchLine}
            visibleLineSet={visibleLineSet}
          />
        </div>
      )}
    </div>
  );
}

function InlineLogView({
  sections,
  showTimestamps = true,
  matchLineSet,
  currentMatchLine,
  visibleLineSet,
}: {
  sections: (LogSection | StepSection)[];
  showTimestamps?: boolean;
  matchLineSet?: Set<number>;
  currentMatchLine?: number;
  visibleLineSet?: Set<number> | null;
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
    if (visibleLineSet && !visibleLineSet.has(key)) return null;
    const tsMatch = line.match(TS_PREFIX_RE);
    const ts = tsMatch ? tsMatch[1] : null;
    const body = tsMatch ? line.slice(tsMatch[0].length) : line;
    const isMatch = matchLineSet?.has(key);
    const isCurrent = isMatch && currentMatchLine === key;
    const matchCls = isCurrent
      ? "bg-yellow-400/30"
      : isMatch
        ? "bg-yellow-400/10"
        : "";
    if (body.trim() === "" && !stepLabel)
      return <div key={key} data-line={key} className={`h-5 ${matchCls}`} />;
    const hasAnsi = body.includes("\x1b[");
    return (
      <div
        key={key}
        data-line={key}
        className={`flex hover:bg-[#161b22] group ${matchCls}`}
      >
        {showTimestamps && ts && (
          <span className="text-[#6e7681] select-none pr-2 shrink-0 tabular-nums">
            {ts}
          </span>
        )}
        {stepLabel && (
          <span className="text-[var(--muted)] shrink-0 pr-2">
            {stepLabel}
            <span className="px-1">│</span>
          </span>
        )}
        {hasAnsi ? (
          <span dangerouslySetInnerHTML={{ __html: ansiToHtml(body) }} />
        ) : (
          <span className={fallbackClass}>{body}</span>
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
  // When set, the matching step bucket auto-expands and scrolls into
  // view. Match is on the stepName suffix of section.name (which is
  // formatted as "<nodeId> · <stepName>"). External selection driver
  // for cross-pane navigation (left nodes panel / StepDag → logs).
  focusStep?: string | null;
  // When set, locates the section containing the absolute line
  // number, expands its step bucket, and scrolls the line into
  // view. Driven by the cross-node global log search.
  focusLine?: number | null;
  // Structured-state lookup for each parsed step bucket. The header
  // renders the step's is_result / has_skip_if / annotation flags as
  // chips alongside the step name, matching the StepDag pill set.
  nodeSteps?: { id: string; is_result?: boolean; has_skip_if?: boolean }[];
}

export default function LogBucketView({
  parsed,
  jobId,
  focusStep,
  focusLine,
  nodeSteps,
}: LogBucketViewProps) {
  const [viewMode, setViewMode] = useState<"steps" | "inline">("steps");
  // Timestamps default on -- they're the cheapest way to correlate
  // log lines with the timeline view, and operators reach for them
  // first when debugging. The toggle parks them when prose-y log
  // bodies dominate a view.
  const [showTimestamps, setShowTimestamps] = useState(true);
  const steps = parsed.sections.filter(
    (s) => s.type === "step",
  ) as StepSection[];

  // Lifted per-step expand state so the header can offer expand/
  // collapse-all. Map key = step index in parsed.sections.
  const [stepOverrides, setStepOverrides] = useState<Record<number, boolean>>(
    {},
  );
  const stepIndices = useMemo(
    () =>
      parsed.sections
        .map((s, i) => (s.type === "step" ? i : -1))
        .filter((i) => i >= 0),
    [parsed],
  );
  const expandAll = () => {
    const next: Record<number, boolean> = {};
    for (const i of stepIndices) next[i] = true;
    setStepOverrides(next);
  };
  const collapseAll = () => {
    const next: Record<number, boolean> = {};
    for (const i of stepIndices) next[i] = false;
    setStepOverrides(next);
    requestAnimationFrame(() => {
      containerRef.current?.scrollIntoView({
        block: "start",
        behavior: "smooth",
      });
    });
  };
  // Build the error-block list: every log line in any section whose
  // body contains an ERROR / error: / FAIL marker. Each block carries
  // its section index (so the walker can auto-expand the containing
  // step bucket) plus the absolute line number (rendered as
  // data-line on the line `<div>`) so scrollIntoView lands on the
  // actual message instead of the section header.
  // Consecutive error lines collapse into one block: a run of 50
  // "ERROR shard-bravo: item N/55 FAILED" lines is one navigation
  // target, not 50. The block's anchor is its first line so scroll
  // lands at the top of the error burst. A non-matching line breaks
  // the run, and the next matching line starts a new block.
  const errorBlocks = useMemo(() => {
    const out: { sectionIdx: number; line: number }[] = [];
    const reError = /\bERROR\b|\berror:|\bFAIL\b|\bpanic:/;
    let lineCursor = 1;
    parsed.sections.forEach((section, idx) => {
      let inBlock = false;
      for (let j = 0; j < section.lines.length; j++) {
        const isErr = reError.test(section.lines[j]);
        if (isErr && !inBlock) {
          out.push({ sectionIdx: idx, line: lineCursor + j });
        }
        inBlock = isErr;
      }
      lineCursor += section.lines.length;
    });
    return out;
  }, [parsed]);
  const errorCursor = useRef(-1);
  const containerRef = useRef<HTMLDivElement>(null);
  const nextError = () => {
    if (errorBlocks.length === 0) return;
    errorCursor.current = (errorCursor.current + 1) % errorBlocks.length;
    const target = errorBlocks[errorCursor.current];
    // Auto-expand the containing step bucket (no-op for between /
    // summary sections, which render their lines inline already).
    setStepOverrides((prev) => ({ ...prev, [target.sectionIdx]: true }));
    requestAnimationFrame(() => {
      const el = containerRef.current?.querySelector(
        `[data-line="${target.line}"]`,
      ) as HTMLElement | null;
      el?.scrollIntoView({ block: "center", behavior: "smooth" });
    });
  };
  // Search: free-text matching across every log line. Lines that
  // contain the query (case-insensitive) get a yellow wash; the
  // active match gets a brighter highlight. Walker mirrors
  // nextError -- expands the containing step bucket and scrolls the
  // line into view. Empty query disables the highlight + walker.
  //
  // Filter mode layers on top: when on, only matching lines (plus
  // `searchContext` neighbors on each side) render; gaps collapse
  // into a "…" separator. Toggling filter off snaps the scroll
  // back to the active match so position survives the mode swap.
  const [searchQuery, setSearchQuery] = useState("");
  const [matchCursor, setMatchCursor] = useState(0);
  const [filterMode, setFilterMode] = useState(false);
  const [searchContext, setSearchContext] = useState(2);
  const searchMatches = useMemo(() => {
    const q = searchQuery.trim().toLowerCase();
    if (!q) return [] as { sectionIdx: number; line: number }[];
    const out: { sectionIdx: number; line: number }[] = [];
    let lineCursor = 1;
    parsed.sections.forEach((section, idx) => {
      for (let j = 0; j < section.lines.length; j++) {
        if (section.lines[j].toLowerCase().includes(q)) {
          out.push({ sectionIdx: idx, line: lineCursor + j });
        }
      }
      lineCursor += section.lines.length;
    });
    return out;
  }, [parsed, searchQuery]);
  const matchLineSet = useMemo(
    () => new Set(searchMatches.map((m) => m.line)),
    [searchMatches],
  );
  // visibleLineSet: when filter mode is on, the union of match lines
  // and their `searchContext` neighbors. null = show everything.
  // LogLines uses this to skip non-matching rows and collapse gaps
  // into a "…" separator.
  const visibleLineSet = useMemo(() => {
    if (!filterMode || searchMatches.length === 0) return null;
    const set = new Set<number>();
    for (const m of searchMatches) {
      for (let d = -searchContext; d <= searchContext; d++) {
        set.add(m.line + d);
      }
    }
    return set;
  }, [filterMode, searchMatches, searchContext]);
  // Reset cursor when the query / match list changes so the next
  // arrow lands on the first hit instead of an index past the end.
  useEffect(() => {
    setMatchCursor(0);
  }, [searchQuery]);
  // Filter + highlight update live as the user types (the render
  // already re-flows from searchMatches), but the view itself only
  // jumps on explicit nav: Enter, Shift+Enter, or the ↑/↓ buttons.
  // Auto-jumping while typing was grabbing the viewport during
  // browsing, which felt wrong.
  const jumpToMatch = (idx: number) => {
    if (searchMatches.length === 0) return;
    const wrapped =
      ((idx % searchMatches.length) + searchMatches.length) %
      searchMatches.length;
    setMatchCursor(wrapped);
    const target = searchMatches[wrapped];
    setStepOverrides((prev) => ({ ...prev, [target.sectionIdx]: true }));
    requestAnimationFrame(() => {
      const el = containerRef.current?.querySelector(
        `[data-line="${target.line}"]`,
      ) as HTMLElement | null;
      el?.scrollIntoView({ block: "center", behavior: "smooth" });
    });
  };
  const nextMatch = () => jumpToMatch(matchCursor + 1);
  const prevMatch = () => jumpToMatch(matchCursor - 1);
  const currentMatchLine =
    searchMatches.length > 0 ? (searchMatches[matchCursor]?.line ?? -1) : -1;
  // External focus-step: when a step is selected elsewhere (left
  // panel row, StepDag click), expand that step's bucket and scroll
  // it into view. Match against the section's parsed step name so
  // the lookup stays in step-id terms.
  // Fire only when focusStep actually changes. The parsed/stepIndices
  // deps would have re-fired this on every streaming-log render,
  // re-snapping the scroll back to the focused step every time the
  // user tried to move. Refs let us read the latest parsed without
  // listing it in the dep array.
  const parsedRef = useRef(parsed);
  parsedRef.current = parsed;
  const stepIndicesRef = useRef(stepIndices);
  stepIndicesRef.current = stepIndices;
  const lastFocusedStep = useRef<string | null>(null);
  useEffect(() => {
    const incoming = focusStep ?? null;
    if (incoming === lastFocusedStep.current) return;
    lastFocusedStep.current = incoming;
    if (!incoming) return;
    const matchIdx = parsedRef.current.sections.findIndex((s) => {
      if (s.type !== "step") return false;
      const name = (s as StepSection).name;
      const stepName = name.includes(" · ")
        ? name.split(" · ").slice(-1)[0]
        : name;
      return stepName === incoming;
    });
    if (matchIdx < 0) return;
    // Single-step-open behavior: collapse every other step bucket
    // so the selected one sits in isolation, mirroring how the
    // outer AllNodesLogs collapses sibling nodes on selection.
    const next: Record<number, boolean> = {};
    for (const i of stepIndicesRef.current) next[i] = i === matchIdx;
    setStepOverrides(next);
    requestAnimationFrame(() => {
      const el = containerRef.current?.querySelector(
        `[data-step-id="${incoming}"]`,
      ) as HTMLElement | null;
      el?.scrollIntoView({ block: "start", behavior: "smooth" });
    });
  }, [focusStep]);
  // Same change-detection trick as focusStep: only act when the
  // incoming focusLine actually changes value (not on every parsed
  // re-render). The walker finds which section contains the line,
  // force-expands it, and scrolls the matching data-line element
  // into view.
  const lastFocusedLine = useRef<number | null>(null);
  useEffect(() => {
    const incoming = focusLine ?? null;
    if (incoming === lastFocusedLine.current) return;
    lastFocusedLine.current = incoming;
    if (incoming == null) return;
    let lineCursor = 1;
    let sectionIdx = -1;
    for (let i = 0; i < parsedRef.current.sections.length; i++) {
      const sec = parsedRef.current.sections[i];
      if (incoming >= lineCursor && incoming < lineCursor + sec.lines.length) {
        sectionIdx = i;
        break;
      }
      lineCursor += sec.lines.length;
    }
    if (sectionIdx < 0) return;
    setStepOverrides((prev) => ({ ...prev, [sectionIdx]: true }));
    requestAnimationFrame(() => {
      const el = containerRef.current?.querySelector(
        `[data-line="${incoming}"]`,
      ) as HTMLElement | null;
      el?.scrollIntoView({ block: "center", behavior: "smooth" });
    });
  }, [focusLine]);
  const scrollToTop = () => {
    containerRef.current?.scrollIntoView({
      block: "start",
      behavior: "smooth",
    });
  };

  const allLines = useMemo(
    () => parsed.sections.flatMap((s) => s.lines),
    [parsed],
  );

  const maxDurationMs = useMemo(() => {
    let max = 0;
    for (const s of parsed.sections) {
      if (s.type === "step") {
        const d = (s as StepSection).durationMs ?? 0;
        if (d > max) max = d;
      }
    }
    return max;
  }, [parsed]);

  // Waterfall extent: earliest step_start to latest step_end across
  // all steps with timestamp data. When we have it, each bar renders
  // positioned along this shared timeline.
  const { waterfallStartMs, waterfallTotalMs } = useMemo(() => {
    let start = Infinity;
    let end = -Infinity;
    for (const s of parsed.sections) {
      if (s.type !== "step") continue;
      const st = s as StepSection;
      if (st.startedAtMs == null) continue;
      if (st.startedAtMs < start) start = st.startedAtMs;
      const finish = st.startedAtMs + (st.durationMs ?? 0);
      if (finish > end) end = finish;
    }
    if (!isFinite(start) || !isFinite(end) || end <= start) {
      return { waterfallStartMs: null, waterfallTotalMs: null };
    }
    return { waterfallStartMs: start, waterfallTotalMs: end - start };
  }, [parsed]);

  let lineOffset = 1;

  return (
    <div
      ref={containerRef}
      className="bg-[#0d1117] border border-[var(--border)] rounded-lg"
    >
      {/* Header */}
      <div className="sticky top-0 z-10 flex items-center gap-2 px-3 py-1.5 border-b border-[var(--border)] bg-[#161b22] rounded-t-lg">
        <span className="text-[10px] text-[var(--muted)] uppercase tracking-wider">
          {steps.length} step{steps.length !== 1 ? "s" : ""}
        </span>
        <input
          type="text"
          value={searchQuery}
          onChange={(e) => setSearchQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              if (e.shiftKey) prevMatch();
              else nextMatch();
            } else if (e.key === "Escape") {
              setSearchQuery("");
            }
          }}
          placeholder="search logs"
          className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-[#0d1117] border border-[var(--border)] focus:border-[var(--accent)] outline-none text-[#c9d1d9] placeholder:text-[var(--muted)] w-40"
        />
        {searchQuery.trim() !== "" && (
          <div className="flex items-center gap-1 text-[10px]">
            <span className="text-[var(--muted)] font-mono tabular-nums">
              {searchMatches.length > 0
                ? `${matchCursor + 1}/${searchMatches.length}`
                : "0/0"}
            </span>
            <button
              onClick={prevMatch}
              disabled={searchMatches.length === 0}
              title="previous match (Shift+Enter)"
              className="px-1.5 py-0.5 rounded text-[var(--muted)] hover:text-[#c9d1d9] hover:bg-[#30363d] transition-colors disabled:opacity-40 disabled:hover:bg-transparent"
            >
              ↑
            </button>
            <button
              onClick={nextMatch}
              disabled={searchMatches.length === 0}
              title="next match (Enter)"
              className="px-1.5 py-0.5 rounded text-[var(--muted)] hover:text-[#c9d1d9] hover:bg-[#30363d] transition-colors disabled:opacity-40 disabled:hover:bg-transparent"
            >
              ↓
            </button>
            <button
              onClick={() => {
                const wasOn = filterMode;
                setFilterMode(!wasOn);
                // Toggling filter off keeps position: scroll back to
                // the current match after the render that re-reveals
                // surrounding lines.
                if (wasOn && currentMatchLine > 0) {
                  requestAnimationFrame(() => {
                    const el = containerRef.current?.querySelector(
                      `[data-line="${currentMatchLine}"]`,
                    ) as HTMLElement | null;
                    el?.scrollIntoView({
                      block: "center",
                      behavior: "smooth",
                    });
                  });
                }
              }}
              title={
                filterMode
                  ? "show all lines"
                  : "hide non-matching lines (filter)"
              }
              className={`px-1.5 py-0.5 rounded transition-colors ${
                filterMode
                  ? "bg-yellow-500/25 text-yellow-200"
                  : "text-[var(--muted)] hover:text-[#c9d1d9] hover:bg-[#30363d]"
              }`}
            >
              filter
            </button>
            {filterMode && (
              <label
                className="flex items-center gap-1 text-[var(--muted)]"
                title="context lines on each side of every match"
              >
                ±
                <select
                  value={searchContext}
                  onChange={(e) => setSearchContext(Number(e.target.value))}
                  className="bg-[#0d1117] border border-[var(--border)] rounded px-1 py-0 text-[10px]"
                >
                  <option value={0}>0</option>
                  <option value={1}>1</option>
                  <option value={2}>2</option>
                  <option value={3}>3</option>
                  <option value={5}>5</option>
                  <option value={10}>10</option>
                </select>
              </label>
            )}
          </div>
        )}
        <span className="flex-1" />
        {viewMode === "steps" && steps.length > 0 && (
          <div className="flex items-center gap-1 text-[10px]">
            {errorBlocks.length > 0 && (
              <button
                onClick={nextError}
                title={`next error message (${errorBlocks.length} match${errorBlocks.length === 1 ? "" : "es"})`}
                className="px-1.5 py-0.5 rounded text-red-300 hover:text-red-200 hover:bg-red-500/20 transition-colors"
              >
                ↓ next error ({errorBlocks.length})
              </button>
            )}
            <button
              onClick={expandAll}
              className="px-1.5 py-0.5 rounded text-[var(--muted)] hover:text-[#c9d1d9] hover:bg-[#30363d] transition-colors"
            >
              expand all
            </button>
            <button
              onClick={collapseAll}
              className="px-1.5 py-0.5 rounded text-[var(--muted)] hover:text-[#c9d1d9] hover:bg-[#30363d] transition-colors"
            >
              collapse all
            </button>
          </div>
        )}
        <button
          onClick={scrollToTop}
          title="scroll to top"
          className="px-1.5 py-0.5 rounded text-[10px] text-[var(--muted)] hover:text-[#c9d1d9] hover:bg-[#30363d] transition-colors"
        >
          ↑ top
        </button>
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
        <button
          onClick={() => setShowTimestamps((v) => !v)}
          title={showTimestamps ? "hide timestamps" : "show timestamps"}
          className={`px-1.5 py-0.5 rounded text-[10px] transition-colors ${showTimestamps ? "bg-[#30363d] text-[#c9d1d9]" : "text-[var(--muted)] hover:text-[#c9d1d9]"}`}
        >
          ts
        </button>
        <CopyButton text={allLines.join("\n")} label="Copy all logs" />
        <DownloadButton
          text={allLines.join("\n")}
          filename={jobId ? `sparkwing-${jobId}.log` : "sparkwing.log"}
        />
      </div>

      {viewMode === "inline" ? (
        <div className="px-3 py-2">
          <InlineLogView
            sections={parsed.sections}
            showTimestamps={showTimestamps}
            matchLineSet={matchLineSet}
            currentMatchLine={currentMatchLine}
            visibleLineSet={visibleLineSet}
          />
        </div>
      ) : (
        parsed.sections.map((section, i) => {
          const offset = lineOffset;
          lineOffset += section.lines.length;

          if (section.type === "step") {
            const override = stepOverrides[i];
            const stepName = stepNameFromSection(section as StepSection);
            const attrs = nodeSteps?.find((s) => s.id === stepName);
            return (
              <StepBucket
                key={i}
                section={section as StepSection}
                lineOffset={offset}
                maxDurationMs={maxDurationMs}
                waterfallStartMs={waterfallStartMs}
                waterfallTotalMs={waterfallTotalMs}
                expanded={filterMode ? true : override}
                showTimestamps={showTimestamps}
                isResult={attrs?.is_result}
                hasSkipIf={attrs?.has_skip_if}
                matchLineSet={matchLineSet}
                currentMatchLine={currentMatchLine}
                visibleLineSet={visibleLineSet}
                onToggle={() =>
                  setStepOverrides((prev) => {
                    const cur =
                      prev[i] ??
                      ((section as StepSection).status === "failed" ||
                        (section as StepSection).status === "running");
                    return { ...prev, [i]: !cur };
                  })
                }
              />
            );
          }
          if (section.type === "summary") {
            return (
              <SummarySection
                key={i}
                section={section}
                lineOffset={offset}
                showTimestamps={showTimestamps}
                matchLineSet={matchLineSet}
                currentMatchLine={currentMatchLine}
                visibleLineSet={visibleLineSet}
              />
            );
          }
          if (section.type === "between" || section.type === "preamble") {
            return (
              <BetweenSection
                key={i}
                section={section}
                lineOffset={offset}
                showTimestamps={showTimestamps}
                matchLineSet={matchLineSet}
                currentMatchLine={currentMatchLine}
                visibleLineSet={visibleLineSet}
              />
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
