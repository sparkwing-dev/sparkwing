import { describe, it } from "node:test";
import assert from "node:assert/strict";
import {
  stripAnsi,
  parseLogLines,
  parseLogSections,
  hasStepBanners,
  type StepSection,
  type LogSection,
} from "./logParser.ts";

// Real ANSI output from the sparkwing CLI
const cyan = "\x1b[36m";
const green = "\x1b[32m";
const red = "\x1b[31m";
const dim = "\x1b[2m";
const bold = "\x1b[1m";
const reset = "\x1b[0m";

describe("stripAnsi", () => {
  it("removes color codes", () => {
    assert.equal(stripAnsi(`${cyan}hello${reset}`), "hello");
  });

  it("removes multiple codes", () => {
    assert.equal(stripAnsi(`${red}${bold}error${reset}`), "error");
  });

  it("passes through clean text", () => {
    assert.equal(stripAnsi("just text"), "just text");
  });

  it("handles dim + reset combos", () => {
    assert.equal(stripAnsi(`${dim}────${reset}`), "────");
  });
});

describe("hasStepBanners", () => {
  it("detects STEP banner in ANSI text", () => {
    assert.equal(hasStepBanners(`${cyan}──── STEP: init ────${reset}`), true);
  });

  it("returns false for plain logs", () => {
    assert.equal(hasStepBanners("just some output\nno banners here"), false);
  });
});

describe("parseJSONLLogs (via parseLogLines auto-detect)", () => {
  const nodeStart = (node: string, ts: string) =>
    JSON.stringify({ ts, level: "info", node, event: "node_start" });
  const stepStart = (node: string, name: string, ts: string) =>
    JSON.stringify({ ts, level: "info", node, event: "step_start", msg: name });
  const stepEnd = (
    node: string,
    name: string,
    outcome: "success" | "failed",
    duration_ms: number,
    ts: string,
  ) =>
    JSON.stringify({
      ts,
      level: outcome === "failed" ? "error" : "info",
      node,
      event: "step_end",
      msg: name,
      attrs: { outcome, duration_ms },
    });
  const stepSkipped = (
    node: string,
    name: string,
    reason: string,
    ts: string,
  ) =>
    JSON.stringify({
      ts,
      level: "info",
      node,
      event: "step_skipped",
      msg: name,
      attrs: { outcome: "skipped", reason },
    });
  const execLine = (node: string, msg: string, ts: string) =>
    JSON.stringify({ ts, level: "info", node, event: "exec_line", msg });
  const nodeEnd = (
    node: string,
    outcome: string,
    duration_ms: number,
    ts: string,
  ) =>
    JSON.stringify({
      ts,
      level: "info",
      node,
      event: "node_end",
      attrs: { outcome, duration_ms },
    });

  it("splits a node into one bucket per step", () => {
    const lines = [
      nodeStart("build", "2026-04-23T00:00:00Z"),
      stepStart("build", "compile", "2026-04-23T00:00:00.100Z"),
      execLine("build", "ok 3 packages", "2026-04-23T00:00:00.200Z"),
      stepEnd("build", "compile", "success", 900, "2026-04-23T00:00:01.000Z"),
      stepStart("build", "push", "2026-04-23T00:00:01.000Z"),
      execLine("build", "pushing image", "2026-04-23T00:00:01.200Z"),
      stepEnd("build", "push", "success", 1000, "2026-04-23T00:00:02.000Z"),
      nodeEnd("build", "success", 2000, "2026-04-23T00:00:02.000Z"),
    ];
    const result = parseLogLines(lines);
    // Two phase buckets (compile, push) + no setup bucket because
    // node went straight into its first step with no prior content.
    assert.equal(result.sections.length, 2);
    const compile = result.sections[0] as StepSection;
    const push = result.sections[1] as StepSection;
    assert.equal(compile.type, "step");
    assert.equal(compile.name, "build · compile");
    assert.equal(compile.status, "passed");
    assert.equal(compile.duration, "900ms");
    assert.equal(push.name, "build · push");
    assert.equal(push.status, "passed");
    assert.equal(push.duration, "1.0s");
    assert.ok(push.lines.some((l) => l.includes("pushing image")));
  });

  it("keeps the in-flight step as 'running' until the next step or node_end", () => {
    const lines = [
      nodeStart("build", "2026-04-23T00:00:00Z"),
      stepStart("build", "compile", "2026-04-23T00:00:00.100Z"),
      execLine("build", "compiling...", "2026-04-23T00:00:00.200Z"),
      // Stream cut here — no step_end / node_end yet.
    ];
    const result = parseLogLines(lines);
    assert.equal(result.sections.length, 1);
    const compile = result.sections[0] as StepSection;
    assert.equal(compile.status, "running");
  });

  it("marks step failed when step_end carries outcome=failed", () => {
    const lines = [
      nodeStart("build", "2026-04-23T00:00:00Z"),
      stepStart("build", "compile", "2026-04-23T00:00:00.100Z"),
      execLine("build", "ok", "2026-04-23T00:00:00.200Z"),
      stepEnd("build", "compile", "success", 900, "2026-04-23T00:00:01.000Z"),
      stepStart("build", "push", "2026-04-23T00:00:01.000Z"),
      execLine("build", "push rejected", "2026-04-23T00:00:01.500Z"),
      stepEnd("build", "push", "failed", 1000, "2026-04-23T00:00:02.000Z"),
      nodeEnd("build", "failed", 2000, "2026-04-23T00:00:02.000Z"),
    ];
    const result = parseLogLines(lines);
    const compile = result.sections[0] as StepSection;
    const push = result.sections[1] as StepSection;
    assert.equal(compile.status, "passed");
    assert.equal(push.status, "failed");
    assert.equal(push.duration, "1.0s");
  });

  it("renders step_skipped as a one-line skipped bucket with reason", () => {
    const lines = [
      nodeStart("build", "2026-04-23T00:00:00Z"),
      stepSkipped(
        "build",
        "deploy",
        "downstream of --stop-at=push",
        "2026-04-23T00:00:00.100Z",
      ),
      nodeEnd("build", "success", 100, "2026-04-23T00:00:00.200Z"),
    ];
    const result = parseLogLines(lines);
    const skipped = result.sections.find(
      (s) => s.type === "step" && s.name === "build · deploy",
    ) as StepSection | undefined;
    assert.ok(skipped, "expected a skipped step bucket for deploy");
    assert.equal(skipped!.status, "passed");
    assert.ok(
      skipped!.lines.some((l) => l.includes("downstream of --stop-at=push")),
    );
  });

  it("treats a no-step node as one bucket (backwards compatible)", () => {
    const lines = [
      nodeStart("test", "2026-04-23T00:00:00Z"),
      execLine("test", "ok pkg/a", "2026-04-23T00:00:00.100Z"),
      execLine("test", "ok pkg/b", "2026-04-23T00:00:00.200Z"),
      nodeEnd("test", "success", 500, "2026-04-23T00:00:00.500Z"),
    ];
    const result = parseLogLines(lines);
    assert.equal(result.sections.length, 1);
    const bucket = result.sections[0] as StepSection;
    assert.equal(bucket.name, "test");
    assert.equal(bucket.status, "passed");
    assert.equal(bucket.duration, "500ms");
  });
});

describe("parseLogLines", () => {
  it("parses a simple passed step", () => {
    const lines = [
      `${cyan}──── STEP: init ────${reset}`,
      "prepare    main@af1a99c",
      "compile    pipeline: release",
      `${green}✓ init (0s) ${dim}────${reset}`,
    ];
    const result = parseLogLines(lines);
    assert.equal(result.sections.length, 1);

    const step = result.sections[0] as StepSection;
    assert.equal(step.type, "step");
    assert.equal(step.name, "init");
    assert.equal(step.status, "passed");
    assert.equal(step.duration, "0s");
    assert.equal(step.lines.length, 2);
    assert.equal(step.lines[0], "prepare    main@af1a99c");
    assert.equal(step.lines[1], "compile    pipeline: release");
  });

  it("parses a failed step with error output", () => {
    const lines = [
      `${cyan}──── STEP: tag and release ────${reset}`,
      "",
      "failed command:",
      'sh -c "git tag v0.15.1"',
      "",
      "output:",
      "fatal: tag 'v0.15.1' already exists",
      "",
      `${red}✗ tag and release (35ms) ${dim}────${reset}`,
    ];
    const result = parseLogLines(lines);
    assert.equal(result.sections.length, 1);

    const step = result.sections[0] as StepSection;
    assert.equal(step.type, "step");
    assert.equal(step.name, "tag and release");
    assert.equal(step.status, "failed");
    assert.equal(step.duration, "35ms");
    assert.ok(step.lines.some((l) => l.includes("already exists")));
  });

  it("parses between-step content", () => {
    const lines = [
      `${cyan}──── STEP: init ────${reset}`,
      "compile    done",
      `${green}✓ init (0s) ${dim}────${reset}`,
      "current  v0.15.0",
      "new      v0.15.1",
      "",
      `${cyan}──── STEP: run tests ────${reset}`,
      `${green}✓ run tests (1.827s) ${dim}────${reset}`,
    ];
    const result = parseLogLines(lines);

    // Should be: step(init), between(current/new), step(run tests)
    assert.equal(result.sections.length, 3);
    assert.equal(result.sections[0].type, "step");
    assert.equal(result.sections[1].type, "between");
    assert.equal(result.sections[2].type, "step");

    const between = result.sections[1] as LogSection;
    assert.ok(between.lines.some((l) => l.includes("current")));
    assert.ok(between.lines.some((l) => l.includes("new")));
  });

  it("parses summary section", () => {
    const lines = [
      `${cyan}──── STEP: build ────${reset}`,
      `${green}✓ build (2s) ${dim}────${reset}`,
      "",
      `${cyan}──── SUMMARY: results ────${reset}`,
      `${green}${bold}✓${reset} build            2s`,
      `${dim}────────────────────${reset}`,
      "                     2s",
    ];
    const result = parseLogLines(lines);

    const types = result.sections.map((s) => s.type);
    assert.ok(types.includes("step"));
    assert.ok(types.includes("summary"));

    const summary = result.sections.find((s) => s.type === "summary")!;
    assert.ok(summary.lines.length > 0);
  });

  it("handles empty input", () => {
    const result = parseLogLines([]);
    assert.equal(result.sections.length, 0);
  });

  it("handles logs with no step banners (plain output)", () => {
    const lines = ["building...", "done"];
    const result = parseLogLines(lines);
    assert.equal(result.sections.length, 1);
    assert.equal(result.sections[0].type, "preamble");
    assert.deepEqual(result.sections[0].lines, ["building...", "done"]);
  });

  it("skips blank-only between sections", () => {
    const lines = [
      `${cyan}──── STEP: a ────${reset}`,
      `${green}✓ a (1s) ${dim}────${reset}`,
      "",
      "",
      `${cyan}──── STEP: b ────${reset}`,
      `${green}✓ b (2s) ${dim}────${reset}`,
    ];
    const result = parseLogLines(lines);

    // Should be just two steps, no between section for blank lines
    assert.equal(result.sections.length, 2);
    assert.equal(result.sections[0].type, "step");
    assert.equal(result.sections[1].type, "step");
  });

  it("marks in-progress step as running when no result line", () => {
    const lines = [
      `${cyan}──── STEP: deploy ────${reset}`,
      "deploying to staging...",
      "waiting for rollout...",
    ];
    const result = parseLogLines(lines);
    assert.equal(result.sections.length, 1);

    const step = result.sections[0] as StepSection;
    assert.equal(step.type, "step");
    assert.equal(step.name, "deploy");
    assert.equal(step.status, "running");
    assert.equal(step.duration, null);
  });

  it("handles step with no duration in result line", () => {
    const lines = [
      `${cyan}──── STEP: quick ────${reset}`,
      `${green}✓ quick ${dim}────────────────${reset}`,
    ];
    const result = parseLogLines(lines);
    assert.equal(result.sections.length, 1);

    const step = result.sections[0] as StepSection;
    assert.equal(step.status, "passed");
    assert.equal(step.duration, "0s"); // fallback for missing duration
  });
});

describe("parseLogSections", () => {
  it("works with real CLI output string", () => {
    // Simulate the exact output from the user's example
    const raw = [
      `${cyan}────────────── STEP: init ──────────────${reset}`,
      "prepare    main@af1a99c",
      "compile    pipeline: release (yaml + jobs/)",
      "compile    cache miss: 2787c952 — compiling",
      "compile    cached at: /Users/test/.sparkwing/cache/2787c952/sparkwing-pipeline",
      `${green}✓ init ${dim}─────────────────────────────────${reset}`,
      "current  v0.15.0",
      "new      v0.15.1",
      "",
      `${cyan}─────────── STEP: run tests ────────────${reset}`,
      `${green}✓ run tests (1.827s) ${dim}───────────────────${reset}`,
      "",
      `${cyan}───────── STEP: update-version ─────────${reset}`,
      `${green}✓ update-version (0s) ${dim}──────────────────${reset}`,
      "",
      `${cyan}──────── STEP: tag and release ─────────${reset}`,
      "",
      "failed command:",
      `sh -c "git add internal/cli/version.go && git commit -m \\"release: v0.15.1\\" && git tag v0.15.1 && git push origin main v0.15.1"`,
      "",
      "output:",
      "[detached HEAD 5ae71e9] release: v0.15.1",
      " 1 file changed, 1 insertion(+), 1 deletion(-)",
      "fatal: tag 'v0.15.1' already exists",
      "",
      `${red}✗ tag and release (35ms) ${dim}───────────────${reset}`,
      "",
      `${cyan}─────────── SUMMARY: results ───────────${reset}`,
      `${green}${bold}✓${reset} run tests            1.827s`,
      `${green}${bold}✓${reset} update-version       0s`,
      `${red}${bold}✗${reset} tag and release      35ms`,
      `${dim}────────────────────────────────${reset}`,
      "                       1.862s",
    ].join("\n");

    const result = parseLogSections(raw);

    // Should have: step(init), between(current/new), step(run tests), step(update-version), step(tag and release), summary
    const steps = result.sections.filter(
      (s) => s.type === "step",
    ) as StepSection[];
    assert.equal(steps.length, 4);

    assert.equal(steps[0].name, "init");
    assert.equal(steps[0].status, "passed");

    assert.equal(steps[1].name, "run tests");
    assert.equal(steps[1].status, "passed");
    assert.equal(steps[1].duration, "1.827s");

    assert.equal(steps[2].name, "update-version");
    assert.equal(steps[2].status, "passed");

    assert.equal(steps[3].name, "tag and release");
    assert.equal(steps[3].status, "failed");
    assert.equal(steps[3].duration, "35ms");

    // Between section with version info
    const between = result.sections.find((s) => s.type === "between");
    assert.ok(between);
    assert.ok(between!.lines.some((l) => l.includes("v0.15.0")));

    // Summary section exists
    const summary = result.sections.find((s) => s.type === "summary");
    assert.ok(summary);
  });
});
