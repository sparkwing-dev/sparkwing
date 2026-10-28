"use client";

import { useEffect, useState } from "react";
import { type PipelineMeta, getPipelines, parseRunArgs, triggerRun } from "@/lib/api";
import { repositoryExample, triggerGit } from "@/lib/triggerSource";
import { useTeamState } from "@/lib/useTeam";

interface TriggerFormProps {
  pipeline?: string;
  onTriggered?: () => void;
  onClose?: () => void;
}

export default function TriggerForm({
  pipeline,
  onTriggered,
  onClose,
}: TriggerFormProps) {
  const [pipelines, setPipelines] = useState<Record<string, PipelineMeta>>({});
  const [selectedPipeline, setSelectedPipeline] = useState(pipeline || "");
  const [argText, setArgText] = useState("");
  const [triggering, setTriggering] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [repository, setRepository] = useState("");
  const [branch, setBranch] = useState("main");
  // A team's runners fetch the source themselves, so a run started here has
  // to name it; a single-team controller's runners take it from the git cache.
  const namesSource = useTeamState().status === "ready";

  useEffect(() => {
    getPipelines().then(setPipelines);
  }, []);

  const args = parseRunArgs(argText);
  // The controller lists pipelines most recently run first, the order worth suggesting.
  const pipelineNames = Object.keys(pipelines);
  const source = namesSource ? triggerGit(repository, branch) : null;
  const sourceProblem =
    source && !source.ok && repository.trim() !== "" ? source.problem : null;

  const handleSubmit = async () => {
    if (!selectedPipeline) return;
    if (source && !source.ok) {
      setError(source.problem);
      return;
    }
    if (!args.ok) {
      setError(args.problem);
      return;
    }
    setTriggering(true);
    setError(null);

    try {
      await triggerRun(
        selectedPipeline,
        args.values,
        source?.ok ? source.value : undefined,
      );
      onTriggered?.();
      onClose?.();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Trigger failed");
    } finally {
      setTriggering(false);
    }
  };

  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-4 space-y-3">
      <div className="flex items-center justify-between">
        <span className="text-xs font-bold uppercase tracking-wider text-[var(--muted)]">
          Run Pipeline
        </span>
        {onClose && (
          <button
            onClick={onClose}
            className="text-xs text-[var(--muted)] hover:text-[var(--foreground)]"
          >
            ✕
          </button>
        )}
      </div>

      {

                                              }
      {!pipeline && (
        <div>
          <label className="text-xs text-[var(--muted)] block mb-1">
            Pipeline
          </label>
          <input
            type="text"
            list="trigger-pipelines"
            aria-label="Pipeline"
            className="w-full bg-[var(--background)] border border-[var(--border)] rounded px-3 py-1.5 text-sm font-mono"
            placeholder="pipeline name, e.g. build"
            value={selectedPipeline}
            onChange={(e) => setSelectedPipeline(e.target.value.trim())}
          />
          <datalist id="trigger-pipelines">
            {pipelineNames.map((name) => (
              <option key={name} value={name} />
            ))}
          </datalist>
        </div>
      )}

      {               }
      {namesSource && (
        <div className="grid grid-cols-1 sm:grid-cols-[1fr_10rem] gap-2">
          <div>
            <label
              htmlFor="trigger-repository"
              className="text-xs text-[var(--muted)] block mb-1"
            >
              Repository
            </label>
            <input
              id="trigger-repository"
              type="url"
              inputMode="url"
              autoComplete="off"
              spellCheck={false}
              className="w-full bg-[var(--background)] border border-[var(--border)] rounded px-3 py-1.5 text-sm font-mono placeholder:text-[var(--muted)]"
              placeholder={repositoryExample}
              value={repository}
              onChange={(e) => setRepository(e.target.value)}
            />
          </div>
          <div>
            <label
              htmlFor="trigger-branch"
              className="text-xs text-[var(--muted)] block mb-1"
            >
              Branch
            </label>
            <input
              id="trigger-branch"
              type="text"
              autoComplete="off"
              spellCheck={false}
              className="w-full bg-[var(--background)] border border-[var(--border)] rounded px-3 py-1.5 text-sm font-mono"
              value={branch}
              onChange={(e) => setBranch(e.target.value)}
            />
          </div>
          <p
            className={`text-xs sm:col-span-2 ${sourceProblem ? "text-yellow-400" : "text-[var(--muted)]"}`}
          >
            {sourceProblem ??
              "A team runner that allows this repository fetches the branch tip and runs it."}
          </p>
        </div>
      )}

      <div>
        <label
          htmlFor="trigger-args"
          className="text-xs text-[var(--muted)] block mb-1"
        >
          Arguments, one name=value per line
        </label>
        <textarea
          id="trigger-args"
          rows={3}
          spellCheck={false}
          className="w-full bg-[var(--background)] border border-[var(--border)] rounded px-3 py-1.5 text-sm font-mono"
          value={argText}
          onChange={(e) => setArgText(e.target.value)}
        />
        {!args.ok && argText.trim() !== "" && (
          <p className="text-xs text-yellow-400">{args.problem}</p>
        )}
      </div>

      {           }
      {error && <p className="text-xs text-red-400">{error}</p>}

      {            }
      <div className="flex items-center gap-2">
        <button
          onClick={handleSubmit}
          disabled={
            !selectedPipeline ||
            triggering ||
            !args.ok ||
            (source !== null && !source.ok)
          }
          className="bg-[var(--accent)] hover:bg-indigo-500 disabled:opacity-50 px-4 py-1.5 rounded text-sm font-medium transition-colors"
        >
          {triggering ? "Triggering..." : "Run"}
        </button>
      </div>
    </div>
  );
}
