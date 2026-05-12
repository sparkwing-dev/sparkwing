"use client";

// SetupPanel renders "how was this run started" -- run id, pipeline,
// trigger source, git context, and the invocation snapshot
// (binary_source, cwd, flags, args, reproducer, hashes) the
// orchestrator persists on the run row at CreateRun time. Mirrors
// the CLI's `--- Setup ---` section so the dashboard surfaces the
// same reproducibility info an operator would see in `wing release`.
//
// Per-node selection info (Runner, Activity, Heartbeat) lives in a
// sibling SelectedNodePanel so the Setup section stays scoped to the
// run as a whole.

import { useState } from "react";
import type { Run, RunInvocation } from "@/lib/api";

// runDurationMs is duplicated here rather than imported so the panel
// stays self-contained and renders cleanly in Storybook / standalone
// previews. Same logic as @/lib/api's runDurationMs.
function durationMs(run: Run): number {
  if (!run.finished_at) return 0;
  return (
    new Date(run.finished_at).getTime() - new Date(run.started_at).getTime()
  );
}

function fmtMs(ms: number): string {
  if (!ms) return "-";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms - m * 60_000) / 1000);
  return `${m}m ${s}s`;
}

// shortHash trims `sha256:<hex>` to its first 12 hex chars for
// display; the full value is exposed via title for copy-on-hover.
// Returns "" when input doesn't look like a sparkwing hash.
function shortHash(h?: string): string {
  if (!h) return "";
  const prefix = "sha256:";
  if (!h.startsWith(prefix)) return h;
  const hex = h.slice(prefix.length);
  return hex.length > 12 ? `sha256:${hex.slice(0, 12)}` : h;
}

// CopyButton: small inline button that copies its `value` to
// clipboard and shows a brief "copied" affordance. Used for
// reproducer + hashes -- the bits an operator/agent typically wants
// to paste elsewhere.
function CopyButton({ value, label }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  const onClick = () => {
    void navigator.clipboard.writeText(value);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };
  return (
    <button
      type="button"
      onClick={onClick}
      className={`text-[10px] uppercase tracking-wider font-mono px-1.5 py-0.5 rounded border transition-colors ${
        copied
          ? "border-green-500/40 text-green-400"
          : "border-[var(--border)] text-[var(--muted)] hover:text-[var(--foreground)] hover:border-[var(--muted)]"
      }`}
      title={`Copy${label ? " " + label : ""}`}
    >
      {copied ? "copied" : (label ?? "copy")}
    </button>
  );
}

// FlagBadge renders one flag entry. Boolean-true gets the flag name;
// scalar values render as `name=value`. Booleans known to be
// "authorization gates" (allow_*, dry_run) get a yellow/red tint so
// they stand out from default knobs like max_parallel.
function FlagBadge({ name, value }: { name: string; value: unknown }) {
  let cls = "border-[var(--border)] text-[var(--muted)]";
  if (typeof value === "boolean" && value) {
    if (
      name === "allow_destructive" ||
      name === "allow_prod" ||
      name === "allow_money"
    ) {
      cls = "border-yellow-500/40 text-yellow-300 bg-yellow-500/10";
    } else if (name === "dry_run") {
      cls = "border-cyan-500/40 text-cyan-300 bg-cyan-500/10";
    } else {
      cls = "border-violet-500/40 text-violet-300 bg-violet-500/10";
    }
  }
  const label = typeof value === "boolean" ? name : `${name}=${String(value)}`;
  return (
    <span
      className={`px-1.5 py-0.5 rounded border text-[11px] font-mono ${cls}`}
    >
      {label}
    </span>
  );
}

// RunLink: jump to another run. Calls onOpenRun if provided (the
// Pipelines page wires this to its sidebar-row click behavior so
// filter state is preserved); falls back to a plain href so the
// component still works in standalone previews.
function RunLink({
  runID,
  cls,
  onOpenRun,
}: {
  runID: string;
  cls: string;
  onOpenRun?: (id: string) => void;
}) {
  if (onOpenRun) {
    return (
      <button
        onClick={() => onOpenRun(runID)}
        className={`font-mono hover:underline ${cls}`}
      >
        #{runID}
      </button>
    );
  }
  return (
    <a
      href={`?run=${encodeURIComponent(runID)}`}
      className={`font-mono hover:underline ${cls}`}
    >
      #{runID}
    </a>
  );
}

// LabelRow is the standard Setup row layout: a fixed-width dim label
// in the gutter and the value column flowing to the right.
function LabelRow({
  label,
  children,
  fieldKey,
  findMatchedFields,
  findActiveKey,
}: {
  label: string;
  children: React.ReactNode;
  fieldKey?: string;
  findMatchedFields?: Set<string>;
  findActiveKey?: string | null;
}) {
  const isFindHit = !!fieldKey && (findMatchedFields?.has(fieldKey) ?? false);
  const isFindCurrent = !!fieldKey && findActiveKey === `setup::${fieldKey}`;
  const findCls = isFindCurrent
    ? "bg-fuchsia-400/30 ring-1 ring-fuchsia-400"
    : isFindHit
      ? "bg-fuchsia-400/10 ring-1 ring-fuchsia-400/60"
      : "";
  return (
    <div
      data-find-key={fieldKey ? `setup::${fieldKey}` : undefined}
      className={`flex items-baseline gap-3 text-xs px-1 -mx-1 rounded ${findCls}`}
    >
      <div className="w-20 shrink-0 text-[var(--muted)] font-mono">{label}</div>
      <div className="min-w-0 flex-1 text-[var(--foreground)] font-mono break-all">
        {children}
      </div>
    </div>
  );
}

export default function SetupPanel({
  run,
  collapsed,
  onToggle,
  onOpenRun,
  inline = false,
  findMatchedFields,
  findActiveKey,
}: {
  run: Run;
  collapsed: boolean;
  onToggle: () => void;
  // Optional callback: when provided, retry-of / retried-as links
  // call onOpenRun(id) instead of navigating via anchor href. The
  // Pipelines page wires this to its sidebar-click behavior so the
  // jump preserves filter state.
  onOpenRun?: (id: string) => void;
  // When true, render body without the collapsible header chevron —
  // the panel is being embedded somewhere (e.g., a tab) where the
  // surrounding UI already names it.
  inline?: boolean;
  // Set of field keys matching the run's top-level find query.
  // Each labeled row checks for its key to paint a fuchsia wash.
  findMatchedFields?: Set<string>;
  findActiveKey?: string | null;
}) {
  const inv: RunInvocation = run.invocation ?? {};
  const flags = inv.flags ?? {};
  const args = inv.args ?? run.args ?? {};
  const flagKeys = Object.keys(flags).sort();
  const argKeys = Object.keys(args).sort();
  const triggerEnvKeys = inv.trigger_env_keys ?? [];

  const commitUrl =
    run.github_owner && run.github_repo && run.git_sha
      ? `https://github.com/${run.github_owner}/${run.github_repo}/commit/${run.git_sha}`
      : null;
  const branchUrl =
    run.github_owner && run.github_repo && run.git_branch
      ? `https://github.com/${run.github_owner}/${run.github_repo}/tree/${run.git_branch}`
      : null;

  return (
    <div className={inline ? "" : "border-b border-[var(--border)] shrink-0"}>
      {!inline && (
        <button
          onClick={onToggle}
          className="w-full flex items-center gap-2 px-4 py-2 text-xs text-[var(--muted)] hover:text-[var(--foreground)] transition-colors"
        >
          <span className="w-4 text-center">{collapsed ? "▸" : "▾"}</span>
          <span className="font-semibold text-[var(--foreground)]">Setup</span>
          {inv.binary_source && (
            <span className="text-[10px] uppercase tracking-wider font-mono text-[var(--muted)]">
              {inv.binary_source}
            </span>
          )}
        </button>
      )}
      {(inline || !collapsed) && (
        <div className="px-4 pb-3 space-y-1">
          <LabelRow
            label="run"
            fieldKey="run-id"
            findMatchedFields={findMatchedFields}
            findActiveKey={findActiveKey}
          >
            <span
              className="cursor-pointer hover:text-cyan-300"
              onClick={() => navigator.clipboard.writeText(run.id)}
              title="copy run id"
            >
              {run.id}
            </span>
          </LabelRow>
          <LabelRow
            label="pipeline"
            fieldKey="pipeline"
            findMatchedFields={findMatchedFields}
            findActiveKey={findActiveKey}
          >
            <span className="text-violet-300">{run.pipeline}</span>
          </LabelRow>
          {run.trigger_source && (
            <LabelRow
              label="trigger"
              fieldKey="trigger"
              findMatchedFields={findMatchedFields}
              findActiveKey={findActiveKey}
            >
              {run.trigger_source}
            </LabelRow>
          )}
          <LabelRow label="started">
            <span>{new Date(run.started_at).toLocaleTimeString()}</span>
            {run.finished_at && (
              <span className="text-[var(--muted)]">
                {" "}
                · duration {fmtMs(durationMs(run))}
              </span>
            )}
          </LabelRow>
          {run.git_sha && (
            <LabelRow
              label="commit"
              fieldKey="commit"
              findMatchedFields={findMatchedFields}
              findActiveKey={findActiveKey}
            >
              {commitUrl ? (
                <a
                  href={commitUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-cyan-400 hover:underline"
                >
                  {run.git_sha.slice(0, 7)}
                </a>
              ) : (
                <span>{run.git_sha.slice(0, 7)}</span>
              )}
            </LabelRow>
          )}
          {run.git_branch && (
            <LabelRow
              label="branch"
              fieldKey="branch"
              findMatchedFields={findMatchedFields}
              findActiveKey={findActiveKey}
            >
              {branchUrl ? (
                <a
                  href={branchUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-amber-400 hover:underline"
                >
                  ⎇ {run.git_branch}
                </a>
              ) : (
                <span className="text-amber-400">⎇ {run.git_branch}</span>
              )}
            </LabelRow>
          )}
          {inv.binary_source && (
            <LabelRow
              label="binary"
              fieldKey="binary"
              findMatchedFields={findMatchedFields}
              findActiveKey={findActiveKey}
            >
              {inv.binary_source}
            </LabelRow>
          )}
          {inv.cwd && (
            <LabelRow
              label="cwd"
              fieldKey="cwd"
              findMatchedFields={findMatchedFields}
              findActiveKey={findActiveKey}
            >
              {inv.cwd}
            </LabelRow>
          )}

          {/* Reproducer is highlighted as the main "copy this command"
               affordance -- agents and humans both want to paste it
               back into a terminal. */}
          {inv.reproducer && (
            <div
              data-find-key="setup::reproducer"
              className={`flex items-center gap-3 text-xs pt-1 px-1 -mx-1 rounded ${
                findActiveKey === "setup::reproducer"
                  ? "bg-fuchsia-400/30 ring-1 ring-fuchsia-400"
                  : findMatchedFields?.has("reproducer")
                    ? "bg-fuchsia-400/10 ring-1 ring-fuchsia-400/60"
                    : ""
              }`}
            >
              <div className="w-20 shrink-0 text-[var(--muted)] font-mono">
                rerun
              </div>
              <div className="min-w-0 flex-1 flex items-center gap-2">
                <code className="px-2 py-1 rounded bg-[var(--surface)] border border-[var(--border)] text-cyan-300 break-all">
                  {inv.reproducer}
                </code>
                <CopyButton value={inv.reproducer} />
              </div>
            </div>
          )}

          {flagKeys.length > 0 && (
            <div className="flex items-baseline gap-3 text-xs pt-2">
              <div className="w-20 shrink-0 text-[var(--muted)] font-mono">
                flags
              </div>
              <div className="min-w-0 flex-1 flex flex-wrap gap-1.5">
                {flagKeys.map((k) => {
                  const key = `setup::flag-${k}`;
                  const isHit = findMatchedFields?.has(`flag-${k}`) ?? false;
                  const isCurrent = findActiveKey === key;
                  return (
                    <span
                      key={k}
                      data-find-key={key}
                      className={`rounded ${
                        isCurrent
                          ? "ring-2 ring-fuchsia-400 bg-fuchsia-400/15"
                          : isHit
                            ? "ring-1 ring-fuchsia-400/60"
                            : ""
                      }`}
                    >
                      <FlagBadge name={k} value={flags[k]} />
                    </span>
                  );
                })}
              </div>
            </div>
          )}

          {argKeys.length > 0 && (
            <div className="flex items-baseline gap-3 text-xs pt-2">
              <div className="w-20 shrink-0 text-[var(--muted)] font-mono">
                args
              </div>
              <div className="min-w-0 flex-1 grid grid-cols-[max-content_1fr] gap-x-3 gap-y-0.5">
                {argKeys.map((k) => {
                  const key = `setup::arg-${k}`;
                  const isHit = findMatchedFields?.has(`arg-${k}`) ?? false;
                  const isCurrent = findActiveKey === key;
                  const findCls = isCurrent
                    ? "bg-fuchsia-400/30 ring-1 ring-fuchsia-400"
                    : isHit
                      ? "bg-fuchsia-400/10 ring-1 ring-fuchsia-400/60"
                      : "";
                  return (
                    <div
                      key={k}
                      data-find-key={key}
                      className={`contents ${findCls}`}
                    >
                      <span
                        className={`text-[var(--muted)] font-mono px-0.5 rounded ${findCls}`}
                      >
                        {k}
                      </span>
                      <span
                        className={`text-[var(--foreground)] font-mono break-all px-0.5 rounded ${findCls}`}
                      >
                        {args[k]}
                      </span>
                    </div>
                  );
                })}
              </div>
            </div>
          )}

          {triggerEnvKeys.length > 0 && (
            <div className="flex items-baseline gap-3 text-xs pt-2">
              <div className="w-20 shrink-0 text-[var(--muted)] font-mono">
                env
              </div>
              <div className="min-w-0 flex-1 flex flex-wrap gap-1.5">
                {triggerEnvKeys.map((k) => {
                  const key = `setup::env-${k}`;
                  const isHit = findMatchedFields?.has(`env-${k}`) ?? false;
                  const isCurrent = findActiveKey === key;
                  return (
                    <span
                      key={k}
                      data-find-key={key}
                      className={`px-1.5 py-0.5 rounded border border-[var(--border)] text-[var(--muted)] text-[11px] font-mono ${
                        isCurrent
                          ? "bg-fuchsia-400/30 ring-1 ring-fuchsia-400"
                          : isHit
                            ? "bg-fuchsia-400/10 ring-1 ring-fuchsia-400/60"
                            : ""
                      }`}
                    >
                      {k}
                    </span>
                  );
                })}
                <span className="text-[10px] text-[var(--muted)] italic">
                  values omitted
                </span>
              </div>
            </div>
          )}

          {(inv.plan_hash || inv.inputs_hash) && (
            <div className="flex items-baseline gap-3 text-xs pt-2">
              <div className="w-20 shrink-0 text-[var(--muted)] font-mono">
                hashes
              </div>
              <div className="min-w-0 flex-1 space-y-0.5">
                {inv.inputs_hash && (
                  <div
                    data-find-key="setup::inputs-hash"
                    className={`flex items-center gap-2 px-1 -mx-1 rounded ${
                      findActiveKey === "setup::inputs-hash"
                        ? "bg-fuchsia-400/30 ring-1 ring-fuchsia-400"
                        : findMatchedFields?.has("inputs-hash")
                          ? "bg-fuchsia-400/10 ring-1 ring-fuchsia-400/60"
                          : ""
                    }`}
                  >
                    <span className="text-[var(--muted)] font-mono w-14">
                      inputs
                    </span>
                    <code
                      className="text-[var(--foreground)] font-mono cursor-pointer hover:text-cyan-300"
                      title={inv.inputs_hash}
                      onClick={() =>
                        navigator.clipboard.writeText(inv.inputs_hash!)
                      }
                    >
                      {shortHash(inv.inputs_hash)}
                    </code>
                  </div>
                )}
                {inv.plan_hash && (
                  <div
                    data-find-key="setup::plan-hash"
                    className={`flex items-center gap-2 px-1 -mx-1 rounded ${
                      findActiveKey === "setup::plan-hash"
                        ? "bg-fuchsia-400/30 ring-1 ring-fuchsia-400"
                        : findMatchedFields?.has("plan-hash")
                          ? "bg-fuchsia-400/10 ring-1 ring-fuchsia-400/60"
                          : ""
                    }`}
                  >
                    <span className="text-[var(--muted)] font-mono w-14">
                      plan
                    </span>
                    <code
                      className="text-[var(--foreground)] font-mono cursor-pointer hover:text-cyan-300"
                      title={inv.plan_hash}
                      onClick={() =>
                        navigator.clipboard.writeText(inv.plan_hash!)
                      }
                    >
                      {shortHash(inv.plan_hash)}
                    </code>
                  </div>
                )}
              </div>
            </div>
          )}

          {(run.retry_of || run.retried_as) && (
            <div className="flex items-baseline gap-3 text-xs pt-2">
              <div className="w-20 shrink-0 text-[var(--muted)] font-mono">
                retry
              </div>
              <div className="min-w-0 flex-1 space-y-0.5 font-mono">
                {run.retry_of && (
                  <div>
                    <span className="text-[var(--muted)]">of</span>{" "}
                    <RunLink
                      runID={run.retry_of}
                      cls="text-cyan-400"
                      onOpenRun={onOpenRun}
                    />
                  </div>
                )}
                {run.retried_as && (
                  <div>
                    <span className="text-[var(--muted)]">as</span>{" "}
                    <RunLink
                      runID={run.retried_as}
                      cls="text-yellow-400"
                      onOpenRun={onOpenRun}
                    />
                  </div>
                )}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
