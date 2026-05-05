"use client";

import { useCallback, useEffect, useState } from "react";
import { getPaused, releaseNode, type PauseState } from "@/lib/api";

// DebugPausePanel lists the open pauses for a run and exposes a
// Release button per row. Copy-paste `sparkwing debug attach` snippet
// sits under each row so an operator can drop into the paused pod
// without leaving the dashboard.
//
// The panel polls every 2s while the run is active; hidden entirely
// when there are no open pauses so it doesn't steal layout space on
// normal runs.
export default function DebugPausePanel({
  runID,
  runStatus,
}: {
  runID: string;
  runStatus: string;
}) {
  const [pauses, setPauses] = useState<PauseState[]>([]);
  const [busy, setBusy] = useState<string>("");
  const [err, setErr] = useState<string>("");

  const refresh = useCallback(async () => {
    try {
      const ps = await getPaused(runID);
      setPauses(ps.filter((p) => !p.released_at));
    } catch {
      // Silent: the panel is supplementary to the main view.
    }
  }, [runID]);

  useEffect(() => {
    refresh();
    if (runStatus !== "running") return;
    const id = setInterval(refresh, 2000);
    return () => clearInterval(id);
  }, [refresh, runStatus]);

  if (pauses.length === 0) return null;

  return (
    <div className="border border-yellow-500/40 bg-yellow-500/5 rounded-md px-3 py-2 text-xs">
      <div className="flex items-center gap-2 mb-1">
        <span className="w-2 h-2 rounded-full bg-yellow-400 animate-pulse" />
        <span className="font-bold uppercase tracking-wider text-yellow-400">
          Paused ({pauses.length})
        </span>
      </div>
      {err && <div className="text-red-400 mb-1">{err}</div>}
      <div className="flex flex-col gap-2">
        {pauses.map((p) => {
          const snippet = `sparkwing debug attach --run ${runID} --node ${p.node_id}`;
          return (
            <div
              key={p.node_id + p.reason}
              className="flex flex-col gap-1 border-t border-yellow-500/20 pt-2 first:border-t-0 first:pt-0"
            >
              <div className="flex items-center gap-2">
                <span className="font-mono text-yellow-300">{p.node_id}</span>
                <span className="text-[var(--muted)]">({p.reason})</span>
                <button
                  className="ml-auto bg-yellow-500/20 hover:bg-yellow-500/30 text-yellow-200 border border-yellow-500/30 px-2 py-0.5 rounded text-[11px]"
                  disabled={busy === p.node_id}
                  onClick={async () => {
                    setErr("");
                    setBusy(p.node_id);
                    try {
                      await releaseNode(runID, p.node_id);
                      await refresh();
                    } catch (e) {
                      setErr(e instanceof Error ? e.message : "release failed");
                    } finally {
                      setBusy("");
                    }
                  }}
                >
                  {busy === p.node_id ? "releasing..." : "Release"}
                </button>
              </div>
              <div
                className="font-mono text-[10px] text-[var(--muted)] cursor-pointer truncate"
                onClick={() => navigator.clipboard.writeText(snippet)}
                title="copy attach command"
              >
                $ {snippet}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
