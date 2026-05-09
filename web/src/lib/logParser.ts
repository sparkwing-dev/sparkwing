// logParser.ts — Parse sparkwing logs into structured sections.
//
// Sparkwing logs come in two on-disk formats:
//
//   * JSONL (current): one LogRecord per line. Envelope fields
//     (ts, level, node, event) are structure; `msg` may contain raw
//     ANSI from child processes. node_start / node_end events bracket
//     each node's output — we map each bracketed span onto a
//     StepSection so the existing LogBucketView renders them as
//     collapsible buckets. run_summary records become the summary
//     section.
//
//   * Legacy STEP banners (pre-rewrite): ANSI-colored text lines
//     like `──── STEP: name ────` / `✓ name (Ns) ────`. Kept so old
//     stored runs stay readable; new runs don't emit this shape.

export type SectionType = "preamble" | "step" | "between" | "summary";

export interface LogSection {
  type: SectionType;
  lines: string[];
}

export interface StepSection extends LogSection {
  type: "step";
  name: string;
  status: "passed" | "failed" | "running";
  duration: string | null;
  durationMs: number | null;
}

export interface ParsedLog {
  sections: (LogSection | StepSection)[];
}

const ANSI_RE = /\x1b\[[0-9;]*m/g;

export function stripAnsi(line: string): string {
  return line.replace(ANSI_RE, "");
}

// ──── STEP: name ────
const STEP_START_RE = /STEP:\s+(.+?)\s*─/;

// ✓ name (1.827s) ──── or ✓ name ────
// Group 1: step name, Group 2: duration (optional)
const STEP_PASS_RE = /^✓\s+(.+?)(?:\s+\(([^)]+)\))?\s+─/;

// ✗ name (35ms) ──── or ✗ name ────
const STEP_FAIL_RE = /^✗\s+(.+?)(?:\s+\(([^)]+)\))?\s+─/;

// ──── SUMMARY: results ────
const SUMMARY_RE = /SUMMARY:/;

function isBlankSection(lines: string[]): boolean {
  return lines.every((l) => l.trim() === "");
}

export interface LogRecord {
  ts?: string;
  level?: string;
  node?: string;
  job?: string;
  job_stack?: string[];
  step?: string;
  event?: string;
  msg?: string;
  attrs?: Record<string, unknown>;
}

function looksLikeJSONL(lines: string[]): boolean {
  for (const l of lines) {
    const t = l.trim();
    if (t === "") continue;
    return t.startsWith("{") && t.endsWith("}");
  }
  return false;
}

// parseJSONLLogs converts a JSONL record stream into the ParsedLog
// shape LogBucketView already knows how to render.
//
// Bucket model:
//   - `node_start` opens a node-scope StepSection named after the
//     node id. Any records that arrive before the first `step` event
//     accumulate into it as the node's "setup" phase.
//   - Each `step` event flushes the currently open bucket (node-
//     scope or a previous step) and opens a new StepSection named
//     `<node> · <step>`. Duration is derived from the delta between
//     consecutive step timestamps (or between the last step and
//     `node_end`).
//   - `node_end` closes the active bucket. If the node itself failed,
//     the *last* bucket inherits the failed status since that's
//     where execution stopped; earlier closed buckets stay passed.
//   - `run_summary` becomes the summary section.
//
// The result is a flat list of StepSections — one per phase when a
// node used Step(), one per node when it didn't. LogBucketView
// renders them identically so no UI changes are needed for the
// Step-aware view.
function parseJSONLLogs(lines: string[]): ParsedLog {
  const sections: (LogSection | StepSection)[] = [];
  let preamble: LogSection = { type: "preamble", lines: [] };

  // Currently-open bucket. Starts as the node-scope setup when
  // node_start fires; each `step` event flushes it and opens a new
  // phase-scope bucket.
  let current: StepSection | null = null;
  let currentStartedAt: number | null = null;
  let currentNode: string = "";
  // Index of the last phase bucket that belonged to the active node,
  // so we can retroactively mark it failed when the node fails. Reset
  // to -1 between nodes.
  let lastPhaseIdx: number = -1;
  // True if the current bucket has seen at least one non-setup record
  // or is itself a phase (post-step). A pure-setup node-scope bucket
  // with no content is dropped on node_end so empty wrapper buckets
  // don't appear in the UI.
  let currentHasContent: boolean = false;

  const pushPreamble = () => {
    if (preamble.lines.length > 0 && !isBlankSection(preamble.lines)) {
      sections.push(preamble);
    }
    preamble = { type: "preamble", lines: [] };
  };

  const parseTS = (ts: string | undefined): number | null => {
    if (!ts) return null;
    const ms = Date.parse(ts);
    return isNaN(ms) ? null : ms;
  };

  // closeCurrent flushes the active bucket with its computed duration
  // relative to `nextTS` (the timestamp of the event that's closing
  // it). `isPhase` marks phase-scope buckets so we can track the
  // last-phase index for node-level failure propagation. `done`
  // means "an explicit boundary is closing this bucket" (next step,
  // node_end, run_summary); stream-tail flushes pass `done=false` so
  // the in-flight step keeps its `running` status — that's what the
  // live log viewer leans on to render the pulsing indigo marker.
  const closeCurrent = (
    nextTS: number | null,
    isPhase: boolean,
    done: boolean,
  ) => {
    if (!current) return;
    if (!currentHasContent && !isPhase) {
      // Drop an empty node-scope setup bucket — a node that went
      // straight into its first step has nothing to show here.
      current = null;
      currentStartedAt = null;
      return;
    }
    if (currentStartedAt != null && nextTS != null) {
      const ms = nextTS - currentStartedAt;
      current.duration = formatDuration(ms);
      current.durationMs = ms;
    }
    if (done && current.status === "running") current.status = "passed";
    sections.push(current);
    if (isPhase) lastPhaseIdx = sections.length - 1;
    current = null;
    currentStartedAt = null;
    currentHasContent = false;
  };

  for (const raw of lines) {
    const t = raw.trim();
    if (t === "") continue;
    let rec: LogRecord;
    try {
      rec = JSON.parse(t);
    } catch {
      if (current) {
        current.lines.push(raw);
        currentHasContent = true;
      } else {
        preamble.lines.push(raw);
      }
      continue;
    }
    const recTS = parseTS(rec.ts);
    switch (rec.event) {
      case "node_start": {
        pushPreamble();
        closeCurrent(recTS, false, true);
        lastPhaseIdx = -1;
        currentNode = rec.node || "node";
        current = {
          type: "step",
          name: currentNode,
          status: "running",
          duration: null,
          durationMs: null,
          lines: [],
        };
        currentStartedAt = recTS;
        currentHasContent = false;
        break;
      }
      case "step_start": {
        // Close the currently-open bucket (node-scope setup or the
        // previous phase) and open a new phase bucket. Setup buckets
        // with no content are dropped; phase buckets always render.
        const stepName = rec.msg || "step";
        closeCurrent(recTS, current?.name !== currentNode, true);
        current = {
          type: "step",
          name: `${currentNode} · ${stepName}`,
          status: "running",
          duration: null,
          durationMs: null,
          lines: [],
        };
        currentStartedAt = recTS;
        currentHasContent = true;
        break;
      }
      case "step_end": {
        // Close the active step bucket with the explicit outcome from
        // attrs.outcome. Duration prefers attrs.duration_ms (set by
        // the SDK) over the timestamp delta so a clock-skewed renderer
        // doesn't drift away from the engine's measurement.
        if (current && current.name !== currentNode) {
          const outcome = (rec.attrs?.outcome as string) || "";
          if (outcome === "failed") current.status = "failed";
          else current.status = "passed";
          const dms = Number(rec.attrs?.duration_ms ?? 0);
          if (dms > 0) {
            current.duration = formatDuration(dms);
            current.durationMs = dms;
          }
          closeCurrent(recTS, true, true);
        }
        break;
      }
      case "step_skipped": {
        // A skipped step closes the previous bucket (if any) and
        // pushes a one-line bucket of its own marked passed (skipped
        // is non-failure). Reason, when present, lands in the bucket
        // body so the UI can show "[range_skip]" alongside the name.
        const stepName = rec.msg || "step";
        closeCurrent(recTS, current?.name !== currentNode, true);
        const reason = (rec.attrs?.reason as string) || "";
        sections.push({
          type: "step",
          name: `${currentNode} · ${stepName}`,
          status: "passed",
          duration: null,
          durationMs: null,
          lines: reason ? [`[skipped: ${reason}]`] : ["[skipped]"],
        });
        lastPhaseIdx = sections.length - 1;
        break;
      }
      case "node_end": {
        const outcome = (rec.attrs?.outcome as string) || "";
        const failed =
          outcome !== "success" &&
          outcome !== "cached" &&
          outcome !== "skipped" &&
          outcome !== "cancelled";
        // Close the active bucket, inheriting the node's outcome on
        // the last phase so the UI highlights the actual failure
        // point rather than every bucket under a failed node.
        if (current) {
          if (failed) current.status = "failed";
          closeCurrent(recTS, current.name !== currentNode, true);
        } else if (failed && lastPhaseIdx >= 0) {
          const last = sections[lastPhaseIdx] as StepSection;
          last.status = "failed";
        }
        currentNode = "";
        lastPhaseIdx = -1;
        break;
      }
      case "run_summary": {
        pushPreamble();
        closeCurrent(recTS, current?.name !== currentNode, true);
        const sumLines = summaryLines(rec);
        sections.push({ type: "summary", lines: sumLines });
        break;
      }
      default: {
        if (current) {
          current.lines.push(recordToLine(rec));
          currentHasContent = true;
        } else {
          preamble.lines.push(recordToLine(rec));
        }
        break;
      }
    }
  }
  // Stream-tail flush: pass done=false so an in-flight step keeps
  // its "running" status for the live viewer. Historical readers
  // that expect final state will either see the explicit node_end
  // (and thus a "passed"/"failed" status set above) or be looking
  // at a truncated log, in which case "running" is the honest label.
  closeCurrent(null, current?.name !== currentNode, false);
  pushPreamble();
  return { sections };
}

// recordToLine produces the in-bucket display text for one log
// record. Node + step are deliberately omitted from the line itself
// because the enclosing StepSection already encodes them in its
// `name` field; repeating them on every line is the "<node> ›
// <step> │" breadcrumb noise that the user sees in the bucket
// view. Job-stack frames (reusable Jobs spawned inside a step)
// are kept since the section name doesn't carry them.
function recordToLine(rec: LogRecord): string {
  const parts: string[] = [];
  const crumb = jobBreadcrumb(rec);
  if (crumb) parts.push(crumb);
  if (rec.event === "retry") parts.push("↻");
  if (rec.level === "error") parts.push("ERROR");
  if (rec.msg) parts.push(rec.msg);
  else if (
    rec.attrs &&
    rec.event !== "step_start" &&
    rec.event !== "step_end"
  ) {
    parts.push(JSON.stringify(rec.attrs));
  }
  return parts.join(" ");
}

// jobBreadcrumb renders only the Job-stack frames -- the section
// header takes care of node/step. A line emitted from a deeply
// spawned Job (Job → SubJob → step) still wants the trace, since
// the section knows nothing about it.
function jobBreadcrumb(rec: LogRecord): string {
  const frames: string[] = [];
  if (rec.job_stack) frames.push(...rec.job_stack);
  if (rec.job) frames.push(rec.job);
  if (frames.length === 0) return "";
  return frames.join(" › ") + " │";
}

// stepNameFromSection extracts the bare step name out of a
// StepSection's `name` field (which is "<node> · <step>" for phase
// buckets, "<node>" for whole-node buckets). The inline view uses
// this to prefix each line with `<step> | <line>` so a flat scroll
// through a multi-step run stays attributable.
export function stepNameFromSection(section: StepSection): string {
  const sep = section.name.indexOf(" · ");
  return sep >= 0 ? section.name.slice(sep + 3) : section.name;
}

function formatDuration(ms: number): string {
  if (!ms || ms < 0) return "0ms";
  if (ms < 1000) return `${ms}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  const mins = Math.floor(s / 60);
  const rem = Math.floor(s % 60);
  return `${mins}m${String(rem).padStart(2, "0")}s`;
}

function summaryLines(rec: LogRecord): string[] {
  const out: string[] = [];
  const status = (rec.attrs?.status as string) || "";
  const dur = Number(rec.attrs?.duration_ms ?? 0);
  out.push(
    `${status === "success" ? "✓" : "✗"} run ${status} (${formatDuration(dur)})`,
  );
  const nodes = (rec.attrs?.nodes as Array<Record<string, unknown>>) || [];
  for (const n of nodes) {
    const id = (n.id as string) || "";
    const oc = (n.outcome as string) || "";
    const d = Number(n.duration_ms ?? 0);
    const icon =
      oc === "success" || oc === "cached"
        ? "✓"
        : oc === "skipped" || oc === "cancelled"
          ? "⊘"
          : "✗";
    out.push(`${icon} ${id.padEnd(32)} ${oc} ${formatDuration(d)}`);
  }
  return out;
}

export function parseLogLines(lines: string[]): ParsedLog {
  if (looksLikeJSONL(lines)) {
    return parseJSONLLogs(lines);
  }
  const sections: (LogSection | StepSection)[] = [];
  let current: LogSection | StepSection = { type: "preamble", lines: [] };

  function pushCurrent() {
    // Skip empty between sections (just blank lines between steps)
    if (current.type === "between" && isBlankSection(current.lines)) return;
    // Skip empty preamble
    if (current.type === "preamble" && isBlankSection(current.lines)) return;
    // Always push steps and summary (a step with no output is still meaningful)
    if (current.type === "step" || current.type === "summary") {
      sections.push(current);
      return;
    }
    if (current.lines.length > 0) {
      sections.push(current);
    }
  }

  for (const raw of lines) {
    const stripped = stripAnsi(raw);

    // Check for step start banner
    const stepMatch = STEP_START_RE.exec(stripped);
    if (stepMatch && stripped.includes("─")) {
      pushCurrent();
      current = {
        type: "step",
        name: stepMatch[1],
        status: "running",
        duration: null,
        durationMs: null,
        lines: [],
      } as StepSection;
      continue;
    }

    // Check for summary banner
    if (SUMMARY_RE.test(stripped) && stripped.includes("─")) {
      pushCurrent();
      current = { type: "summary", lines: [] };
      continue;
    }

    // Check for step pass result line
    const passMatch = STEP_PASS_RE.exec(stripped);
    if (passMatch && current.type === "step") {
      (current as StepSection).status = "passed";
      (current as StepSection).duration = passMatch[2] ?? "0s";
      pushCurrent();
      current = { type: "between", lines: [] };
      continue;
    }

    // Check for step fail result line
    const failMatch = STEP_FAIL_RE.exec(stripped);
    if (failMatch && current.type === "step") {
      (current as StepSection).status = "failed";
      (current as StepSection).duration = failMatch[2] ?? "0s";
      pushCurrent();
      current = { type: "between", lines: [] };
      continue;
    }

    // Regular line — keep the raw bytes so SGR escapes survive into
    // the renderer (LogLines paints them via ansiToHtml). The
    // `stripped` form above is used only for banner regex matching.
    current.lines.push(raw);
  }

  pushCurrent();
  return { sections };
}

export function parseLogSections(rawLog: string): ParsedLog {
  return parseLogLines(rawLog.split("\n"));
}

export function hasStepBanners(rawLog: string): boolean {
  const stripped = stripAnsi(rawLog);
  return STEP_START_RE.test(stripped);
}
