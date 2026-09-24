import Link from "next/link";
import type { Node as RunNode } from "@/lib/api";
import {
  compactExecutionDisplay,
  executionAttemptOrdinal,
  executionAttempts,
  executionAttemptsNewestFirst,
  executionDisplay,
} from "@/lib/executionAttribution";
import { fmtDateTime } from "@/lib/timeFormat";

export function ExecutionIconPaths({ icon }: { icon: NonNullable<ReturnType<typeof executionDisplay>["icon"]> }) {
  const drawing = {
    machine: <><rect x="2" y="3" width="16" height="11" rx="1.5" /><path d="M1 17h18M7 14l-1 3m8-3 1 3" /></>,
    cloud: <path d="M5 15H4a3 3 0 0 1-.2-6A5.5 5.5 0 0 1 14.5 7a4 4 0 1 1 .5 8H5Z" />,
    github: <><path d="M10 1.5a8.5 8.5 0 0 0-2.7 16.6v-2.5c-2.4.5-3-1-3-1-.4-1-.9-1.3-.9-1.3-.8-.5.1-.5.1-.5.9.1 1.4.9 1.4.9.8 1.3 2 1 2.5.8.1-.6.4-1 .7-1.3-2.1-.2-4.3-1-4.3-4.7 0-1 .3-1.8.9-2.5-.1-.3-.4-1.3.1-2.5 0 0 1 0 2.6 1.4a9 9 0 0 1 4.8 0c1.6-1.4 2.6-1.4 2.6-1.4.5 1.2.2 2.2.1 2.5.6.7.9 1.5.9 2.5 0 3.7-2.2 4.5-4.3 4.7.5.4.8 1.1.8 2.1v3.2A8.5 8.5 0 0 0 10 1.5Z" /></>,
    cluster: <><rect x="3" y="2" width="14" height="7" rx="1" /><rect x="3" y="11" width="14" height="7" rx="1" /><path d="M6 5.5h.01M6 14.5h.01M9 5.5h5M9 14.5h5" /></>,
  }[icon];
  return drawing;
}

export function ExecutionLocationIcon({
  display,
  size = 14,
}: {
  display: ReturnType<typeof executionDisplay>;
  size?: number;
}) {
  if (!display.icon || !display.tooltip) return null;
  return (
    <span className="group relative inline-flex shrink-0 items-center text-slate-400" tabIndex={0} aria-label={display.tooltip}>
      <svg width={size} height={size} viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><ExecutionIconPaths icon={display.icon} /></svg>
      <span role="tooltip" className="pointer-events-none absolute bottom-full left-0 z-50 mb-1 hidden w-max max-w-64 rounded border border-slate-600 bg-slate-900 px-2 py-1 text-xs normal-case text-slate-100 shadow-lg group-hover:block group-focus:block">{display.tooltip}</span>
    </span>
  );
}

export function ExecutionBadge({ node }: { node: RunNode }) {
  const display = compactExecutionDisplay(node);
  return display ? <ExecutionLocationIcon display={display} /> : null;
}

export function ExecutionAttributionPanel({ node }: { node: RunNode }) {
  const attempts = executionAttempts(node);
  const newestFirst = executionAttemptsNewestFirst(node);
  if (attempts.length === 0) return null;

  return (
    <section
      className="rounded border border-[var(--border)] bg-[var(--surface)] px-3 py-2"
      aria-label={`Execution history for ${node.id}`}
    >
      <div className="mb-1 text-xs font-semibold text-[var(--foreground)]">
        Execution history
      </div>
        <ol className="space-y-1">
          {newestFirst.map((attempt, index) => {
            const display = executionDisplay(attempt);
            const ordinal = executionAttemptOrdinal(attempt);
            return (
              <li
                key={`${attempt.run_id || "unknown-run"}:${attempt.node_id || node.id}:${ordinal ?? "unknown"}:${attempt.executor_name || ""}:${attempt.started_at || ""}:${index}`}
                className={`flex flex-wrap items-center gap-x-2 gap-y-0.5 rounded px-2 py-1 text-xs ${display.className}`}
              >
                <ExecutionLocationIcon display={display} />
                <span className="font-mono text-[10px] opacity-80">
                  {ordinal == null ? "Attempt unsequenced" : `Attempt ${ordinal}`}
                </span>
                <span className="font-mono">{display.executorLabel}</span>
                {display.platformLabel && (
                  <span className="text-[10px] opacity-80">
                    Platform {display.platformLabel}
                  </span>
                )}
                {attempt.run_id && (
                  <span className="text-[10px]">
                    Run{" "}
                    <Link
                      href={`/runs?run=${encodeURIComponent(attempt.run_id)}`}
                      aria-label={`Open execution run ${attempt.run_id}`}
                      className="font-mono underline underline-offset-2"
                    >
                      {attempt.run_id}
                    </Link>
                  </span>
                )}
                {attempt.started_at && <span className="text-[10px] opacity-80">started {fmtDateTime(attempt.started_at)}</span>}
                {attempt.finished_at && <span className="text-[10px] opacity-80">ended {fmtDateTime(attempt.finished_at)}</span>}
                {attempt.outcome && <span className="text-[10px]">outcome {attempt.outcome}</span>}
                {attempt.failure_reason && <span className="text-[10px]">failure {attempt.failure_reason}</span>}
                {attempt.retry_run_id && (
                  <span className="text-[10px]">
                    Retry continued in{" "}
                    <Link
                      href={`/runs?run=${encodeURIComponent(attempt.retry_run_id)}`}
                      aria-label={`Open retry run ${attempt.retry_run_id}`}
                      className="font-mono underline underline-offset-2"
                    >
                      {attempt.retry_run_id}
                    </Link>
                  </span>
                )}
              </li>
            );
          })}
        </ol>
    </section>
  );
}
