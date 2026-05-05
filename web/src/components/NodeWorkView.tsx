"use client";

import type {
  Node as RunNode,
  NodeWork,
  NodeWorkSpawn,
  NodeWorkSpawnEach,
  NodeWorkStep,
  NodeModifiers,
} from "@/lib/api";

// NodeWorkView renders the inner DAG that lives inside a Plan-level
// Node: Steps with their Needs, plus SpawnNode and SpawnNodeForEach
// declarations. The shape is sourced from the plan snapshot
// (server-side eager Plan-time materialization), so the full
// reachable tree is visible before the run dispatches.
//
// Step status comes from the per-node log stream (step_start /
// step_end events), but the dashboard already polls those. Here we
// only need the structural picture; live status is rendered by the
// adjacent log pane.
export default function NodeWorkView({ node }: { node: RunNode }) {
  if (!node.work && !node.modifiers) {
    return null;
  }
  return (
    <div className="text-xs space-y-2">
      {node.modifiers && <ModifiersChips m={node.modifiers} />}
      {node.work && (
        <div className="border border-[var(--border)] rounded p-2 bg-[#0d1117]">
          <WorkTree work={node.work} />
        </div>
      )}
    </div>
  );
}

function ModifiersChips({ m }: { m: NodeModifiers }) {
  const chips: { label: string; cls: string }[] = [];
  if (m.retry && m.retry > 0) {
    const verb = m.retry_auto ? "Retry(auto)" : "Retry";
    let label = `${verb}=${m.retry}`;
    if (m.retry_backoff_ms && m.retry_backoff_ms > 0) {
      label += `@${fmtDuration(m.retry_backoff_ms)}`;
    }
    chips.push({ label, cls: "bg-amber-500/20 text-amber-300" });
  }
  if (m.timeout_ms && m.timeout_ms > 0) {
    chips.push({
      label: `Timeout=${fmtDuration(m.timeout_ms)}`,
      cls: "bg-purple-500/20 text-purple-300",
    });
  }
  if (m.runs_on && m.runs_on.length > 0) {
    chips.push({
      label: `RunsOn=${m.runs_on.join(",")}`,
      cls: "bg-cyan-500/20 text-cyan-300",
    });
  }
  if (m.cache_key) {
    let label = `Cache=${m.cache_key}`;
    if (m.cache_max && m.cache_max > 1) {
      label += `(max=${m.cache_max})`;
    }
    chips.push({ label, cls: "bg-emerald-500/20 text-emerald-300" });
  }
  if (m.inline) {
    chips.push({ label: "Inline", cls: "bg-slate-500/30 text-slate-200" });
  }
  if (m.optional) {
    chips.push({ label: "Optional", cls: "bg-slate-500/30 text-slate-200" });
  }
  if (m.continue_on_error) {
    chips.push({
      label: "ContinueOnError",
      cls: "bg-slate-500/30 text-slate-200",
    });
  }
  if (m.on_failure) {
    chips.push({
      label: `OnFailure=${m.on_failure}`,
      cls: "bg-red-500/20 text-red-300",
    });
  }
  if (m.has_before_run) {
    chips.push({ label: "BeforeRun", cls: "bg-slate-500/30 text-slate-200" });
  }
  if (m.has_after_run) {
    chips.push({ label: "AfterRun", cls: "bg-slate-500/30 text-slate-200" });
  }
  if (m.has_skip_if) {
    chips.push({ label: "SkipIf", cls: "bg-slate-500/30 text-slate-200" });
  }
  if (chips.length === 0) return null;
  return (
    <div className="flex flex-wrap gap-1">
      {chips.map((c) => (
        <span
          key={c.label}
          className={`px-1.5 py-0.5 rounded text-[10px] font-mono ${c.cls}`}
        >
          {c.label}
        </span>
      ))}
    </div>
  );
}

function WorkTree({ work }: { work: NodeWork }) {
  const empty =
    (!work.steps || work.steps.length === 0) &&
    (!work.spawns || work.spawns.length === 0) &&
    (!work.spawn_each || work.spawn_each.length === 0);
  if (empty) {
    return <div className="text-[var(--muted)] italic">(empty Work)</div>;
  }
  return (
    <ul className="space-y-1 font-mono">
      {work.steps?.map((s) => (
        <li key={s.id}>
          <StepRow step={s} />
        </li>
      ))}
      {work.spawns?.map((sp) => (
        <li key={sp.id}>
          <SpawnRow spawn={sp} />
        </li>
      ))}
      {work.spawn_each?.map((each) => (
        <li key={each.id}>
          <SpawnEachRow each={each} />
        </li>
      ))}
    </ul>
  );
}

function StepRow({ step }: { step: NodeWorkStep }) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="w-1.5 h-1.5 rounded-full bg-blue-400 shrink-0" />
      <span className="text-blue-200">Step</span>
      <span className="text-[var(--foreground)]">{step.id}</span>
      {step.is_result && (
        <span className="px-1 py-0.5 rounded text-[9px] font-bold uppercase tracking-wider bg-emerald-500/20 text-emerald-300">
          result
        </span>
      )}
      {step.has_skip_if && (
        <span className="px-1 py-0.5 rounded text-[9px] font-bold uppercase tracking-wider bg-slate-500/30 text-slate-300">
          skip_if
        </span>
      )}
      {step.needs && step.needs.length > 0 && (
        <span className="text-[var(--muted)]">
          needs: {step.needs.join(", ")}
        </span>
      )}
    </div>
  );
}

function SpawnRow({ spawn }: { spawn: NodeWorkSpawn }) {
  return (
    <div>
      <div className="flex flex-wrap items-center gap-2">
        {/* Ringed dot signals the layer-jump and the suspended-runner
            cost — distinct from the filled Step dot. */}
        <span className="w-1.5 h-1.5 rounded-full bg-fuchsia-400 ring-1 ring-fuchsia-200 shrink-0" />
        <span className="text-fuchsia-200">SpawnNode</span>
        <span className="text-[var(--foreground)]">{spawn.id}</span>
        {spawn.target_job && (
          <span className="text-[var(--muted)]">job={spawn.target_job}</span>
        )}
        {spawn.has_skip_if && (
          <span className="px-1 py-0.5 rounded text-[9px] font-bold uppercase tracking-wider bg-slate-500/30 text-slate-300">
            skip_if
          </span>
        )}
        {spawn.needs && spawn.needs.length > 0 && (
          <span className="text-[var(--muted)]">
            needs: {spawn.needs.join(", ")}
          </span>
        )}
      </div>
      {spawn.target_work && (
        <div className="ml-4 mt-1 pl-2 border-l border-fuchsia-500/30">
          <WorkTree work={spawn.target_work} />
        </div>
      )}
    </div>
  );
}

function SpawnEachRow({ each }: { each: NodeWorkSpawnEach }) {
  return (
    <div>
      <div className="flex flex-wrap items-center gap-2">
        <span className="w-1.5 h-1.5 rounded-full bg-fuchsia-400 ring-1 ring-fuchsia-200 shrink-0" />
        <span className="text-fuchsia-200">SpawnNodeForEach</span>
        <span className="text-[var(--foreground)]">{each.id}</span>
        <span className="px-1 py-0.5 rounded text-[9px] font-bold uppercase tracking-wider bg-fuchsia-500/20 text-fuchsia-300">
          per item
        </span>
        {each.target_job && (
          <span className="text-[var(--muted)]">job={each.target_job}</span>
        )}
        {each.needs && each.needs.length > 0 && (
          <span className="text-[var(--muted)]">
            needs: {each.needs.join(", ")}
          </span>
        )}
      </div>
      {each.note && (
        <div className="ml-4 text-[10px] italic text-[var(--muted)]">
          {each.note}
        </div>
      )}
      {each.item_template_work && (
        <div className="ml-4 mt-1 pl-2 border-l border-fuchsia-500/30">
          <WorkTree work={each.item_template_work} />
        </div>
      )}
    </div>
  );
}

function fmtDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)}s`;
  if (ms < 3_600_000) return `${Math.round(ms / 60_000)}m`;
  return `${Math.round(ms / 3_600_000)}h`;
}
