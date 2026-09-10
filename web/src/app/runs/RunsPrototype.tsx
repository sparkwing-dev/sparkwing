"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import type { Run, RunDetail } from "@/lib/api";
import { runDurationMs } from "@/lib/api";
import { fmtAgo, fmtFullDate, fmtMs } from "@/lib/timeFormat";
import {
  repoLabel,
  type RunFilterState,
  clearAllFilters,
} from "@/components/RunFilters";
import Tooltip from "@/components/Tooltip";
import "./prototype.css";

const variants = [
  {
    key: "focus",
    name: "A · Focus inbox",
    question: "What needs my attention?",
  },
  {
    key: "workbench",
    name: "B · Investigation",
    question: "Why did this run fail?",
  },
  {
    key: "board",
    name: "C · Pipeline board",
    question: "Which pipelines are healthy?",
  },
];
const live = (r: Run) =>
  ["running", "claimed", "pending", "paused"].includes(r.status);
const passed = (r: Run) => ["success", "complete"].includes(r.status);
const identity = (r: Run) => `${repoLabel(r)}/${r.pipeline}`;
const tone = (r: Run) =>
  passed(r)
    ? "good"
    : r.status === "failed"
      ? "bad"
      : live(r)
        ? "active"
        : "muted";
const duration = (r: Run) =>
  r.finished_at ? fmtMs(runDurationMs(r)) : "in progress";

type Props = {
  runs: Run[];
  allRuns: Run[];
  selected: string | null;
  detail: RunDetail | null;
  onSelect: (id: string | null) => void;
  filters: RunFilterState;
};

export default function RunsPrototype(props: Props) {
  const { onSelect } = props;
  const params = useSearchParams();
  const router = useRouter();
  const index = Math.max(
    0,
    variants.findIndex((v) => v.key === params.get("variant")),
  );
  const variant = variants[index];
  const [lens, setLens] = useState("all");
  const [showHistory, setShowHistory] = useState(false);
  const filtered = props.runs.filter(
    (r) =>
      variant.key !== "workbench" ||
      lens === "all" ||
      (lens === "failed"
        ? r.status === "failed"
        : lens === "live"
          ? live(r)
          : passed(r)),
  );
  const groups = new Map<string, Run[]>();
  for (const run of filtered)
    groups.set(identity(run), [...(groups.get(identity(run)) ?? []), run]);
  const latest = [...groups.values()].map((rs) => rs[0]);
  const attention = latest.filter(
    (r) => r.status === "failed" || r.status === "paused",
  );
  const selected =
    props.runs.find((r) => r.id === props.selected) ??
    props.detail?.run ??
    null;
  const triggers = [
    ...new Set(
      props.allRuns
        .map((r) => r.trigger_source)
        .filter((v): v is string => !!v),
    ),
  ].sort();
  const setVariant = (next: number) => {
    const query = new URLSearchParams(params.toString());
    query.set(
      "variant",
      variants[(next + variants.length) % variants.length].key,
    );
    router.replace(`/runs?${query}`, { scroll: false });
  };
  useEffect(() => {
    const handle = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement;
      if (target.closest("input, textarea, select, [contenteditable]")) return;
      if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
        event.preventDefault();
        const query = new URLSearchParams(window.location.search);
        query.set(
          "variant",
          variants[(index + (event.key === "ArrowRight" ? 1 : 2)) % 3].key,
        );
        router.replace(`/runs?${query}`, { scroll: false });
      }
      if (event.key === "Escape") onSelect(null);
    };
    window.addEventListener("keydown", handle);
    return () => window.removeEventListener("keydown", handle);
  }, [index, router, onSelect]);

  const inspector = (
    <RunInspector
      run={selected}
      detail={props.detail}
      onClose={() => props.onSelect(null)}
    />
  );
  return (
    <div className="ui-prototype">
      <header className="proto-header">
        <div>
          <div className="proto-eyebrow">Runs / Design exploration</div>
          <h1>{variant.question}</h1>
          <p>
            Live data · latest {props.allRuns.length} runs · read-only preview
          </p>
        </div>
        <div className="proto-header-links">
          <Link href="/runs">Current layout ↗</Link>
          <Link href="/crons">Crons ↗</Link>
        </div>
      </header>
      <div className="proto-toolbar">
        <label className="proto-search">
          <span aria-hidden="true">⌕</span>
          <input
            aria-label="Find runs"
            placeholder="Find a pipeline, repository or branch…"
            value={props.filters.filterText}
            onChange={(e) => props.filters.setFilterText(e.target.value)}
          />
        </label>
        <select
          aria-label="Trigger source"
          value={props.filters.filterTrigger[0] ?? ""}
          onChange={(e) =>
            props.filters.setFilterTrigger(
              e.target.value ? [e.target.value] : [],
            )
          }
        >
          <option value="">All triggers</option>
          {triggers.map((t) => (
            <option key={t}>{t}</option>
          ))}
        </select>
        <button
          className="proto-button"
          onClick={() => {
            clearAllFilters(props.filters);
            setLens("all");
          }}
        >
          Reset filters
        </button>
        <span className="proto-muted">{filtered.length} matching runs</span>
      </div>

      {variant.key === "focus" && (
        <div className="proto-focus">
          <div className="proto-metrics">
            <Metric
              label="Latest run failed"
              value={attention.filter((r) => r.status === "failed").length}
              note="Across matching pipelines"
              tone="bad"
            />
            <Metric
              label="In flight"
              value={filtered.filter(live).length}
              note="Running, queued or paused"
              tone="active"
            />
            <Metric
              label="Latest run passed"
              value={latest.filter(passed).length}
              note="Repeated successes folded away"
              tone="good"
            />
          </div>
          <div className="proto-focus-columns">
            <section>
              <div className="proto-section-heading">
                <h2>Start here</h2>
                <span>Latest run per repository / pipeline</span>
              </div>
              {attention.length ? (
                attention.map((run) => (
                  <button
                    className="proto-incident"
                    key={run.id}
                    onClick={() => props.onSelect(run.id)}
                  >
                    <span className="proto-incident-icon">!</span>
                    <span className="proto-incident-body">
                      <span className="proto-eyebrow">
                        {repoLabel(run)} · {run.status}
                      </span>
                      <strong>{run.pipeline}</strong>
                      <span className="proto-error-preview">
                        {run.error ||
                          "Open the run to inspect its failed steps."}
                      </span>
                      <span className="proto-muted">
                        {run.git_branch || "No branch recorded"} ·{" "}
                        {run.trigger_source || "Unknown trigger"} ·{" "}
                        {fmtAgo(run.started_at)}
                      </span>
                    </span>
                    <span className="proto-open">Investigate →</span>
                  </button>
                ))
              ) : (
                <div className="proto-empty">
                  No matching pipeline has a failed latest run.
                </div>
              )}
              <button
                className="proto-history-toggle"
                onClick={() => setShowHistory(!showHistory)}
              >
                {showHistory ? "Hide" : "Show"} full activity ·{" "}
                {filtered.length} runs
              </button>
              {showHistory && (
                <RunList
                  runs={filtered}
                  selected={props.selected}
                  onSelect={props.onSelect}
                />
              )}
            </section>
            <aside>
              {selected ? (
                inspector
              ) : (
                <>
                  <div className="proto-section-heading">
                    <h2>Recently active</h2>
                    <span>One row per pipeline</span>
                  </div>
                  <RunList
                    runs={latest.slice(0, 8)}
                    selected={props.selected}
                    onSelect={props.onSelect}
                  />
                  <div className="proto-note">
                    A pipeline with a later successful run drops out of Start
                    here. This view groups by repository and pipeline, across
                    branches.
                  </div>
                </>
              )}
            </aside>
          </div>
        </div>
      )}

      {variant.key === "workbench" && (
        <div className="proto-workbench">
          <aside className="proto-views">
            <div className="proto-eyebrow">Quick views</div>
            {[
              ["all", "All activity"],
              ["failed", "Failed runs"],
              ["live", "In flight"],
              ["passed", "Passed"],
            ].map(([key, label]) => (
              <button
                key={key}
                aria-pressed={lens === key}
                className={lens === key ? "is-active" : ""}
                onClick={() => setLens(key)}
              >
                {label}
                <span>
                  {
                    props.runs.filter(
                      (r) =>
                        key === "all" ||
                        (key === "failed"
                          ? r.status === "failed"
                          : key === "live"
                            ? live(r)
                            : passed(r)),
                    ).length
                  }
                </span>
              </button>
            ))}
            <p>
              Choose a run. Its cause, steps and exact timing stay alongside the
              list.
            </p>
          </aside>
          <section className="proto-run-column">
            <div className="proto-section-heading">
              <h2>Activity</h2>
              <span>{filtered.length} runs</span>
            </div>
            <RunList
              runs={filtered}
              selected={props.selected}
              onSelect={props.onSelect}
            />
          </section>
          <section className="proto-detail-column">{inspector}</section>
        </div>
      )}

      {variant.key === "board" && (
        <div className="proto-board-wrap">
          <div className="proto-board">
            {[
              {
                name: "Needs a look",
                kind: "bad",
                rows: latest.filter(
                  (r) => r.status === "failed" || r.status === "paused",
                ),
              },
              {
                name: "In flight",
                kind: "active",
                rows: latest.filter((r) => live(r) && r.status !== "paused"),
              },
              {
                name: "Latest passed",
                kind: "good",
                rows: latest.filter(passed),
              },
              {
                name: "Other outcomes",
                kind: "muted",
                rows: latest.filter(
                  (r) => !passed(r) && !live(r) && r.status !== "failed",
                ),
              },
            ].map((column) => (
              <section className="proto-board-lane" key={column.name}>
                <div className="proto-section-heading">
                  <h2>
                    <i className={`proto-dot ${column.kind}`} />
                    {column.name}
                  </h2>
                  <span>{column.rows.length}</span>
                </div>
                {column.rows.map((run) => {
                  const history = groups.get(identity(run)) ?? [];
                  return (
                    <article className="proto-pipeline-card" key={run.id}>
                      <button
                        className="proto-pipeline-title"
                        onClick={() => props.onSelect(run.id)}
                      >
                        <span className="proto-eyebrow">{repoLabel(run)}</span>
                        <strong>{run.pipeline}</strong>
                      </button>
                      <div className="proto-card-meta">
                        <span className={`proto-status ${tone(run)}`}>
                          {run.status}
                        </span>
                        <span>{fmtAgo(run.started_at)}</span>
                      </div>
                      <History runs={history} onSelect={props.onSelect} />
                      <div className="proto-card-meta">
                        <span>{history.length} runs in window</span>
                        <span>
                          {history.filter((r) => r.status === "failed").length}{" "}
                          failed
                        </span>
                      </div>
                      {run.error && (
                        <p className="proto-error-preview">{run.error}</p>
                      )}
                    </article>
                  );
                })}
                {!column.rows.length && (
                  <div className="proto-lane-empty">No matching pipelines</div>
                )}
              </section>
            ))}
          </div>
          {selected && (
            <aside className="proto-board-inspector">{inspector}</aside>
          )}
        </div>
      )}

      <div className="proto-switcher" aria-label="Prototype variants">
        <button
          aria-label="Previous variant"
          onClick={() => setVariant(index - 1)}
        >
          ←
        </button>
        <div>
          <strong>{variant.name}</strong>
          <span>Prototype · {index + 1}/3 · use ← →</span>
        </div>
        <button aria-label="Next variant" onClick={() => setVariant(index + 1)}>
          →
        </button>
      </div>
    </div>
  );
}

function Metric({
  label,
  value,
  note,
  tone,
}: {
  label: string;
  value: number;
  note: string;
  tone: string;
}) {
  return (
    <div className="proto-metric">
      <span className="proto-eyebrow">{label}</span>
      <strong className={tone}>{value}</strong>
      <span className="proto-muted">{note}</span>
    </div>
  );
}
function RunList({
  runs,
  selected,
  onSelect,
}: {
  runs: Run[];
  selected: string | null;
  onSelect: (id: string) => void;
}) {
  if (!runs.length)
    return <div className="proto-empty">No runs match these filters.</div>;
  return (
    <div className="proto-run-list">
      {runs.map((run) => (
        <button
          key={run.id}
          data-prototype-run={run.id}
          aria-pressed={selected === run.id}
          className={`proto-run ${selected === run.id ? "is-selected" : ""}`}
          onClick={() => onSelect(run.id)}
        >
          <i className={`proto-dot ${tone(run)}`} />
          <span className="proto-run-text">
            <span className="proto-eyebrow">
              {repoLabel(run)} · {run.trigger_source || "unknown trigger"}
            </span>
            <strong>{run.pipeline}</strong>
            <span className="proto-muted">
              {run.git_branch || "No branch recorded"}
            </span>
          </span>
          <span className="proto-run-time">
            <span className={`proto-status ${tone(run)}`}>{run.status}</span>
            <Tooltip content={fmtFullDate(run.started_at)}>
              <span>{fmtAgo(run.started_at)}</span>
            </Tooltip>
            <span>{duration(run)}</span>
          </span>
        </button>
      ))}
    </div>
  );
}
function History({
  runs,
  onSelect,
}: {
  runs: Run[];
  onSelect: (id: string) => void;
}) {
  return (
    <div className="proto-history" aria-label="Recent runs, oldest to newest">
      {runs
        .slice(0, 24)
        .reverse()
        .map((run) => (
          <Tooltip
            key={run.id}
            content={`${run.status} · ${fmtFullDate(run.started_at)} · ${duration(run)}`}
          >
            <button
              aria-label={`Open ${run.status} run ${run.id}`}
              className={`proto-bar ${tone(run)}`}
              onClick={() => onSelect(run.id)}
            />
          </Tooltip>
        ))}
    </div>
  );
}
function RunInspector({
  run,
  detail,
  onClose,
}: {
  run: Run | null;
  detail: RunDetail | null;
  onClose: () => void;
}) {
  const [tab, setTab] = useState("cause");
  if (!run)
    return (
      <div className="proto-inspector-empty">
        <span>↖</span>
        <h2>Keep your place.</h2>
        <p>
          Select a run to inspect its outcome and steps without replacing the
          activity list.
        </p>
      </div>
    );
  const active = detail?.run.id === run.id ? detail : null;
  const failed =
    active?.nodes.filter(
      (n) => n.status === "failed" || n.outcome === "failed",
    ) ?? [];
  const explanation = failed[0]?.error || run.error;
  return (
    <div className="proto-inspector" aria-label="Run investigation">
      <div className="proto-inspector-heading">
        <div>
          <span className="proto-eyebrow">{repoLabel(run)}</span>
          <h2>{run.pipeline}</h2>
        </div>
        <button aria-label="Close investigation" onClick={onClose}>
          ×
        </button>
      </div>
      <div className="proto-inspector-meta">
        <span className={`proto-status ${tone(run)}`}>{run.status}</span>
        <span>{duration(run)}</span>
        <span>{run.trigger_source || "unknown trigger"}</span>
      </div>
      <div className="proto-inspector-tabs">
        {["cause", "steps", "context"].map((t) => (
          <button key={t} onClick={() => setTab(t)} aria-pressed={tab === t}>
            {t === "cause"
              ? "Outcome"
              : t === "steps"
                ? `Steps${active ? ` (${active.nodes.length})` : ""}`
                : "Context"}
          </button>
        ))}
      </div>
      {tab === "cause" && (
        <div className="proto-inspector-content">
          <span className="proto-eyebrow">
            {run.status === "failed" ? "First failed step" : "Run outcome"}
          </span>
          <h3>
            {failed[0]?.id ||
              (passed(run)
                ? "Completed successfully"
                : active
                  ? run.status
                  : "Loading run details…")}
          </h3>
          {explanation ? (
            <pre className="proto-failure-text">{explanation}</pre>
          ) : (
            <p className="proto-muted">
              {passed(run)
                ? "All recorded work completed. Open Steps to see the execution breakdown."
                : "No additional error recorded."}
            </p>
          )}
          {failed.length > 1 && (
            <p className="proto-muted">
              {failed.length - 1} other failed nodes. See Steps for the full
              set.
            </p>
          )}
        </div>
      )}
      {tab === "steps" && (
        <div className="proto-inspector-content">
          {!active ? (
            <p>Loading steps…</p>
          ) : (
            active.nodes.map((node) => (
              <details className="proto-node" key={node.id}>
                <summary>
                  <span>{node.id}</span>
                  <span>{node.status}</span>
                </summary>
                {node.error && <pre>{node.error}</pre>}
                <p>
                  {node.deps.length
                    ? `Depends on ${node.deps.join(", ")}`
                    : "No dependencies"}{" "}
                  · {fmtMs(node.duration_ms)}
                </p>
              </details>
            ))
          )}
        </div>
      )}
      {tab === "context" && (
        <dl className="proto-context">
          <dt>Run</dt>
          <dd>{run.id}</dd>
          <dt>Branch</dt>
          <dd>{run.git_branch || "Not recorded"}</dd>
          <dt>Commit</dt>
          <dd>{run.git_sha || "Not recorded"}</dd>
          <dt>Started</dt>
          <dd>{fmtFullDate(run.started_at)}</dd>
          <dt>Finished</dt>
          <dd>
            {run.finished_at ? fmtFullDate(run.finished_at) : "Not finished"}
          </dd>
        </dl>
      )}
      <Link
        className="proto-full-run"
        href={`/runs?run=${encodeURIComponent(run.id)}`}
      >
        Open full run, logs and timeline ↗
      </Link>
    </div>
  );
}
