import Link from "next/link";
import type { Node as RunNode } from "@/lib/api";
import {
  executionAttemptOrdinal,
  executionAttempts,
  executionAttemptsNewestFirst,
  executionDisplay,
} from "@/lib/executionAttribution";
import { fmtDateTime } from "@/lib/timeFormat";

function locationIcon(location: string): string {
  if (location === "local") return "⌂";
  if (location === "cloud") return "☁";
  return "?";
}

export function ExecutionBadge({ node }: { node: RunNode }) {
  const attempts = executionAttempts(node);
  if (attempts.length === 0 && !node.claimed && !node.started_at) return null;
  const display = executionDisplay(attempts.at(-1));
  return (
    <span
      className={`inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[9px] font-semibold uppercase tracking-wide ${display.className}`}
      aria-label={`Execution location: ${display.locationLabel}; executor: ${display.executorLabel}`}
      title={`${display.locationLabel} · ${display.executorLabel}`}
    >
      <span aria-hidden>{locationIcon(display.location)}</span>
      <span>{display.locationLabel}</span>
    </span>
  );
}

export function ExecutionAttributionPanel({ node }: { node: RunNode }) {
  const attempts = executionAttempts(node);
  const newestFirst = executionAttemptsNewestFirst(node);
  if (attempts.length === 0 && !node.claimed && !node.started_at) return null;

  return (
    <section
      className="border-b border-[var(--border)] bg-[var(--surface)] px-4 py-2"
      aria-label={`Execution history for ${node.id}`}
    >
      <div className="mb-1.5 text-[10px] font-bold uppercase tracking-wider text-[var(--muted)]">
        Execution history
      </div>
      {attempts.length === 0 ? (
        <div className="text-xs text-slate-300">
          <span className="font-semibold">Location unknown.</span> This execution
          predates durable executor attribution.
        </div>
      ) : (
        <ol className="flex flex-wrap gap-2">
          {newestFirst.map((attempt, index) => {
            const display = executionDisplay(attempt);
            const ordinal = executionAttemptOrdinal(attempt);
            return (
              <li
                key={`${attempt.run_id || "unknown-run"}:${attempt.node_id || node.id}:${ordinal ?? "unknown"}:${attempt.executor_name || ""}:${attempt.started_at || ""}:${index}`}
                className={`min-w-56 rounded border px-2.5 py-2 text-xs ${display.className}`}
              >
                <div className="flex items-center gap-1.5">
                  <span aria-hidden>{locationIcon(display.location)}</span>
                  <span className="font-semibold">{display.locationLabel}</span>
                  <span className="ml-auto font-mono text-[10px] opacity-80">
                    {ordinal == null ? "Attempt unsequenced" : `Attempt ${ordinal}`}
                  </span>
                </div>
                <div className="mt-1 font-mono text-[11px]">
                  {display.executorLabel}
                </div>
                {display.platformLabel && (
                  <div className="mt-1 text-[10px] opacity-80">
                    Platform {display.platformLabel}
                  </div>
                )}
                {attempt.run_id && (
                  <div className="mt-1 text-[10px]">
                    Run{" "}
                    <Link
                      href={`/runs?run=${encodeURIComponent(attempt.run_id)}`}
                      aria-label={`Open execution run ${attempt.run_id}`}
                      className="font-mono underline underline-offset-2"
                    >
                      {attempt.run_id}
                    </Link>
                  </div>
                )}
                <div className="mt-1 flex flex-wrap gap-x-2 text-[10px] opacity-80">
                  {attempt.started_at && (
                    <span>started {fmtDateTime(attempt.started_at)}</span>
                  )}
                  {attempt.finished_at && (
                    <span>ended {fmtDateTime(attempt.finished_at)}</span>
                  )}
                  {attempt.outcome && <span>outcome {attempt.outcome}</span>}
                  {attempt.failure_reason && (
                    <span>failure {attempt.failure_reason}</span>
                  )}
                </div>
                {attempt.retry_run_id && (
                  <div className="mt-1 text-[10px]">
                    Retry continued in{" "}
                    <Link
                      href={`/runs?run=${encodeURIComponent(attempt.retry_run_id)}`}
                      aria-label={`Open retry run ${attempt.retry_run_id}`}
                      className="font-mono underline underline-offset-2"
                    >
                      {attempt.retry_run_id}
                    </Link>
                  </div>
                )}
              </li>
            );
          })}
        </ol>
      )}
    </section>
  );
}
