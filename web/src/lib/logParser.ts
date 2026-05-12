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
  // startedAtMs is the unix-millis at which the step began, parsed
  // from the step_start log record's timestamp. Null when the parser
  // didn't observe a step_start (older logs, manual phase buckets).
  startedAtMs: number | null;
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

  // Node-scope bucket -- holds lines emitted between node_start and
  // the first step_start, or after step_end before node_end. Lives
  // separately from step buckets so it doesn't have to share state.
  let nodeScope: StepSection | null = null;
  let nodeScopeStartedAt: number | null = null;
  let nodeScopeHasContent = false;
  let currentNode: string = "";
  // Index of the most recently started step section in `sections`.
  // Used for retroactive failure attribution at node_end when no
  // explicit step_end:failed lands.
  let lastPhaseIdx: number = -1;
  // Parallel-aware: the orchestrator runs steps concurrently inside
  // one node (parallel shard pattern), so we keep every in-flight
  // step in a map keyed on step id. Log records carry rec.step set
  // inside the step body, which we use to route lines to the right
  // bucket. step_start pushes a section into `sections` immediately
  // so order reflects start-order; step_end finalizes the bucket in
  // place and removes it from the map.
  const openSteps = new Map<
    string,
    { section: StepSection; startedAtMs: number | null }
  >();

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

  // closeNodeScope flushes the node-scope setup bucket. `isFinal`
  // means an explicit boundary (node_end / run_summary); stream-
  // tail flushes pass false so a still-mid-flight setup keeps its
  // "running" marker.
  const closeNodeScope = (nextTS: number | null, isFinal: boolean) => {
    if (!nodeScope) return;
    if (!nodeScopeHasContent) {
      nodeScope = null;
      nodeScopeStartedAt = null;
      return;
    }
    if (nodeScopeStartedAt != null && nextTS != null) {
      const ms = nextTS - nodeScopeStartedAt;
      nodeScope.duration = formatDuration(ms);
      nodeScope.durationMs = ms;
    }
    if (isFinal && nodeScope.status === "running") nodeScope.status = "passed";
    sections.push(nodeScope);
    nodeScope = null;
    nodeScopeStartedAt = null;
    nodeScopeHasContent = false;
  };

  // closeStep finalizes one step bucket by id. Outcome comes from
  // step_end.attrs (preferred); when null (node_end sweep or stream-
  // tail), the bucket keeps its current status unless `done` is true
  // and it's still "running" -- then we promote to passed.
  const closeStep = (
    stepID: string,
    nextTS: number | null,
    outcome: string | null,
    durationMs: number | null,
    done: boolean,
  ) => {
    const entry = openSteps.get(stepID);
    if (!entry) return;
    const sec = entry.section;
    if (outcome === "failed") sec.status = "failed";
    else if (outcome === "success") sec.status = "passed";
    else if (outcome === "skipped") sec.status = "passed";
    else if (done && sec.status === "running") sec.status = "passed";
    if (durationMs != null && durationMs > 0) {
      sec.durationMs = durationMs;
      sec.duration = formatDuration(durationMs);
    } else if (entry.startedAtMs != null && nextTS != null) {
      const ms = nextTS - entry.startedAtMs;
      sec.durationMs = ms;
      sec.duration = formatDuration(ms);
    }
    openSteps.delete(stepID);
  };

  const closeAllOpenSteps = (
    nextTS: number | null,
    failNode: boolean,
    done: boolean,
  ) => {
    for (const k of Array.from(openSteps.keys())) {
      if (failNode) {
        const entry = openSteps.get(k);
        if (entry && entry.section.status === "running") {
          entry.section.status = "failed";
        }
      }
      closeStep(k, nextTS, null, null, done);
    }
  };

  for (const raw of lines) {
    const t = raw.trim();
    if (t === "") continue;
    let rec: LogRecord;
    try {
      rec = JSON.parse(t);
    } catch {
      // Non-JSON line: route to node-scope (or preamble before any
      // node has started). Can't attribute to a parallel step
      // because there's no rec.step on a raw text line.
      if (nodeScope) {
        nodeScope.lines.push(raw);
        nodeScopeHasContent = true;
      } else {
        preamble.lines.push(raw);
      }
      continue;
    }
    const recTS = parseTS(rec.ts);
    switch (rec.event) {
      case "node_start": {
        pushPreamble();
        closeAllOpenSteps(recTS, false, true);
        closeNodeScope(recTS, true);
        lastPhaseIdx = -1;
        currentNode = rec.node || "node";
        nodeScope = {
          type: "step",
          name: currentNode,
          status: "running",
          duration: null,
          durationMs: null,
          startedAtMs: recTS,
          lines: [],
        };
        nodeScopeStartedAt = recTS;
        nodeScopeHasContent = false;
        break;
      }
      case "step_start": {
        // First step_start of the node flushes the setup bucket (it
        // had its chance to collect pre-step lines). Subsequent
        // step_starts that fire while the node-scope is already null
        // are no-ops on closeNodeScope. Each step pushes its own
        // section now so the rendered order matches start-order.
        closeNodeScope(recTS, false);
        const stepID = rec.msg || "step";
        const sec: StepSection = {
          type: "step",
          name: `${currentNode} · ${stepID}`,
          status: "running",
          duration: null,
          durationMs: null,
          startedAtMs: recTS,
          lines: [],
        };
        sections.push(sec);
        lastPhaseIdx = sections.length - 1;
        openSteps.set(stepID, { section: sec, startedAtMs: recTS });
        break;
      }
      case "step_end": {
        const stepID = rec.msg || "";
        const outcome = (rec.attrs?.outcome as string) || null;
        const dms = Number(rec.attrs?.duration_ms ?? 0);
        closeStep(stepID, recTS, outcome, dms > 0 ? dms : null, true);
        break;
      }
      case "step_skipped": {
        const stepID = rec.msg || "step";
        const reason = (rec.attrs?.reason as string) || "";
        sections.push({
          type: "step",
          name: `${currentNode} · ${stepID}`,
          status: "passed",
          duration: null,
          durationMs: null,
          startedAtMs: recTS,
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
        // Any step still running at node_end inherits the node's
        // failure. If everything was already closed and the node
        // still failed (e.g. failed in the dispatch envelope), tag
        // the most recently started step so the UI has somewhere
        // to surface the red marker.
        if (openSteps.size > 0) {
          closeAllOpenSteps(recTS, failed, true);
        } else if (failed && lastPhaseIdx >= 0) {
          const last = sections[lastPhaseIdx] as StepSection;
          if (last.status === "running") last.status = "failed";
        }
        if (nodeScope) {
          if (failed && nodeScope.status === "running")
            nodeScope.status = "failed";
          closeNodeScope(recTS, true);
        }
        currentNode = "";
        lastPhaseIdx = -1;
        break;
      }
      case "run_summary": {
        pushPreamble();
        closeAllOpenSteps(recTS, false, true);
        closeNodeScope(recTS, true);
        const sumLines = summaryLines(rec);
        sections.push({ type: "summary", lines: sumLines });
        break;
      }
      default: {
        // Route the line to its owning step bucket via rec.step.
        // Falls back to the node-scope bucket when the record has
        // no step attribution (between-step Info logs, etc.).
        const stepID = rec.step || "";
        const entry = stepID ? openSteps.get(stepID) : null;
        if (entry) {
          entry.section.lines.push(recordToLine(rec));
        } else if (nodeScope) {
          nodeScope.lines.push(recordToLine(rec));
          nodeScopeHasContent = true;
        } else {
          preamble.lines.push(recordToLine(rec));
        }
        break;
      }
    }
  }
  // Stream-tail flush: keep open steps in their current status
  // (typically "running") for the live viewer. Tail-flushes pass
  // done=false so an in-flight step doesn't get prematurely promoted
  // to "passed" before its actual step_end arrives.
  closeAllOpenSteps(null, false, false);
  closeNodeScope(null, false);
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
  const ts = fmtTSInline(rec.ts);
  if (ts) parts.push(ts);
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

// fmtTSInline renders a record's timestamp as a bracketed
// HH:MM:SS.mmm prefix. The renderer detects this fixed shape and
// either keeps it visible or strips it when the user toggles
// timestamps off; baking it into the line keeps parseLogLines'
// shape (string[]) intact.
function fmtTSInline(ts?: string): string {
  if (!ts) return "";
  const d = new Date(ts);
  if (isNaN(d.getTime())) return "";
  const h = String(d.getHours()).padStart(2, "0");
  const m = String(d.getMinutes()).padStart(2, "0");
  const s = String(d.getSeconds()).padStart(2, "0");
  const ms = String(d.getMilliseconds()).padStart(3, "0");
  return `[${h}:${m}:${s}.${ms}]`;
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
        startedAtMs: null,
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
