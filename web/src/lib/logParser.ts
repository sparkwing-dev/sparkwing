
export type SectionType = "preamble" | "step" | "between" | "summary";

export interface LogSection {
  type: SectionType;
  lines: string[];
}

export interface StepSection extends LogSection {
  type: "step";
  name: string;
  status: "passed" | "failed" | "cancelled" | "running";
  duration: string | null;
  durationMs: number | null;
  startedAtMs: number | null;
}

export interface ParsedLog {
  sections: (LogSection | StepSection)[];
}

const ANSI_RE = /\x1b\[[0-9;]*m/g;

export function stripAnsi(line: string): string {
  return line.replace(ANSI_RE, "");
}

const STEP_START_RE = /STEP:\s+(.+?)\s*─/;

const STEP_PASS_RE = /^✓\s+(.+?)(?:\s+\(([^)]+)\))?\s+─/;

const STEP_FAIL_RE = /^✗\s+(.+?)(?:\s+\(([^)]+)\))?\s+─/;

const SUMMARY_RE = /SUMMARY:/;

function isBlankSection(lines: string[]): boolean {
  return lines.every((l) => l.trim() === "");
}

export interface LogRecord {
  ts?: string;
  level?: string;
  node?: string;
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

function parseJSONLLogs(lines: string[]): ParsedLog {
  const sections: (LogSection | StepSection)[] = [];
  let preamble: LogSection = { type: "preamble", lines: [] };

  let nodeScope: StepSection | null = null;
  let nodeScopeStartedAt: number | null = null;
  let nodeScopeHasContent = false;
  let currentNode: string = "";
  let lastPhaseIdx: number = -1;
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
    else if (outcome === "cancelled") sec.status = "cancelled";
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
  closeAllOpenSteps(null, false, false);
  closeNodeScope(null, false);
  pushPreamble();
  return { sections };
}

function recordToLine(rec: LogRecord): string {
  const parts: string[] = [];
  const ts = fmtTSInline(rec.ts);
  if (ts) parts.push(ts);
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

function fmtTSInline(ts?: string): string {
  if (!ts) return "";
  const d = new Date(ts);
  if (isNaN(d.getTime())) return "";
  const date = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
  const h = String(d.getHours()).padStart(2, "0");
  const m = String(d.getMinutes()).padStart(2, "0");
  const s = String(d.getSeconds()).padStart(2, "0");
  const ms = String(d.getMilliseconds()).padStart(3, "0");
  return `[${date} ${h}:${m}:${s}.${ms}]`;
}

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
    if (current.type === "between" && isBlankSection(current.lines)) return;
    if (current.type === "preamble" && isBlankSection(current.lines)) return;
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

    if (SUMMARY_RE.test(stripped) && stripped.includes("─")) {
      pushCurrent();
      current = { type: "summary", lines: [] };
      continue;
    }

    const passMatch = STEP_PASS_RE.exec(stripped);
    if (passMatch && current.type === "step") {
      (current as StepSection).status = "passed";
      (current as StepSection).duration = passMatch[2] ?? "0s";
      pushCurrent();
      current = { type: "between", lines: [] };
      continue;
    }

    const failMatch = STEP_FAIL_RE.exec(stripped);
    if (failMatch && current.type === "step") {
      (current as StepSection).status = "failed";
      (current as StepSection).duration = failMatch[2] ?? "0s";
      pushCurrent();
      current = { type: "between", lines: [] };
      continue;
    }

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
