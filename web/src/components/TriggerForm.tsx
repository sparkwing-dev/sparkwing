"use client";

import { useEffect, useState } from "react";
import {
  type PipelineArg,
  type PipelineMeta,
  getPipelines,
  triggerJob,
} from "@/lib/api";

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
  const [argValues, setArgValues] = useState<Record<string, string>>({});
  const [triggering, setTriggering] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    getPipelines().then(setPipelines);
  }, []);

  // Reset arg values when pipeline changes
  useEffect(() => {
    const meta = pipelines[selectedPipeline];
    if (!meta?.args) {
      setArgValues({});
      return;
    }
    const defaults: Record<string, string> = {};
    for (const arg of meta.args) {
      if (arg.default) defaults[arg.name] = arg.default;
    }
    setArgValues(defaults);
  }, [selectedPipeline, pipelines]);

  const meta = pipelines[selectedPipeline];
  const args = meta?.args || [];
  const pipelineNames = Object.keys(pipelines).sort();

  const handleSubmit = async () => {
    if (!selectedPipeline) return;
    setTriggering(true);
    setError(null);

    // Collect non-empty args
    const argsToSend: Record<string, string> = {};
    for (const arg of args) {
      const val = argValues[arg.name] || "";
      if (val) argsToSend[arg.name] = val;
    }

    try {
      await triggerJob(selectedPipeline, {
        args: Object.keys(argsToSend).length > 0 ? argsToSend : undefined,
      });
      onTriggered?.();
      onClose?.();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Trigger failed");
    } finally {
      setTriggering(false);
    }
  };

  const missingRequired = args.some((a) => a.required && !argValues[a.name]);

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

      {/* Pipeline selector (only if not pre-selected). Falls back to
          a free-text input until Session C lights up /api/v1/pipelines
          and pipelineNames gets populated. */}
      {!pipeline && (
        <div>
          <label className="text-xs text-[var(--muted)] block mb-1">
            Pipeline
          </label>
          {pipelineNames.length > 0 ? (
            <select
              className="w-full bg-[var(--background)] border border-[var(--border)] rounded px-3 py-1.5 text-sm"
              value={selectedPipeline}
              onChange={(e) => setSelectedPipeline(e.target.value)}
            >
              <option value="">Select a pipeline...</option>
              {pipelineNames.map((name) => (
                <option key={name} value={name}>
                  {name}
                </option>
              ))}
            </select>
          : (
            <input
              type="text"
              className="w-full bg-[var(--background)] border border-[var(--border)] rounded px-3 py-1.5 text-sm font-mono"
              placeholder="pipeline name, e.g. demo-fast"
              value={selectedPipeline}
              onChange={(e) => setSelectedPipeline(e.target.value)}
            />
          )}
        </div>
      )}

      {/* Args form */}
      {args.length > 0 && (
        <div className="space-y-2">
          {args.map((arg) => (
            <ArgInput
              key={arg.name}
              arg={arg}
              value={argValues[arg.name] || ""}
              onChange={(val) =>
                setArgValues((prev) => ({ ...prev, [arg.name]: val }))
              }
            />
          ))}
        </div>
      )}

      {/* No args message */}
      {selectedPipeline && args.length === 0 && (
        <p className="text-xs text-[var(--muted)]">
          {Object.keys(pipelines).length === 0
            ? "No pipeline metadata available yet — run a pipeline first to discover its args."
            : "This pipeline takes no arguments."}
        </p>
      )}

      {/* Error */}
      {error && <p className="text-xs text-red-400">{error}</p>}

      {/* Submit */}
      <div className="flex items-center gap-2">
        <button
          onClick={handleSubmit}
          disabled={!selectedPipeline || triggering || missingRequired}
          className="bg-[var(--accent)] hover:bg-indigo-500 disabled:opacity-50 px-4 py-1.5 rounded text-sm font-medium transition-colors"
        >
          {triggering ? "Triggering..." : "Run"}
        </button>
        {missingRequired && (
          <span className="text-xs text-yellow-400">Fill required args</span>
        )}
      </div>
    </div>
  );
}

function ArgInput({
  arg,
  value,
  onChange,
}: {
  arg: PipelineArg;
  value: string;
  onChange: (val: string) => void;
}) {
  if (arg.type === "bool") {
    return (
      <label className="flex items-center gap-2 text-xs cursor-pointer">
        <input
          type="checkbox"
          checked={value === "true"}
          onChange={(e) => onChange(e.target.checked ? "true" : "")}
          className="rounded border-[var(--border)] bg-[var(--background)]"
        />
        <span className="font-mono text-[#c9d1d9]">{arg.name}</span>
        {arg.desc && <span className="text-[var(--muted)]">— {arg.desc}</span>}
      </label>
    );
  }

  return (
    <div>
      <label className="text-xs text-[var(--muted)] block mb-1">
        <span className="font-mono">{arg.name}</span>
        {arg.required && <span className="text-red-400 ml-1">*</span>}
        {arg.desc && <span className="ml-2">{arg.desc}</span>}
      </label>
      <input
        type={arg.type === "int" ? "number" : "text"}
        placeholder={arg.default || arg.desc || arg.name}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="w-full bg-[var(--background)] border border-[var(--border)] rounded px-3 py-1.5 text-sm font-mono placeholder:text-[var(--muted)]"
      />
    </div>
  );
}
