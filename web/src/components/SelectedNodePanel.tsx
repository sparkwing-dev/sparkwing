"use client";

import type { Node as RunNode } from "@/lib/api";
import { parseHolder } from "@/lib/api";
import { HeartbeatLabel } from "@/components/HeartbeatDot";
import StatusLabel from "@/components/StatusLabel";
import FailureReasonBadge from "@/components/FailureReasonBadge";

function fmtMs(ms: number): string {
  if (!ms) return "";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms - m * 60_000) / 1000);
  return `${m}m ${s}s`;
}

function elapsed(node: RunNode): string {
  if (node.duration_ms) return fmtMs(node.duration_ms);
  if (node.started_at) {
    const ms = Date.now() - new Date(node.started_at).getTime();
    return fmtMs(ms);
  }
  return "";
}

export default function SelectedNodePanel({ node }: { node: RunNode }) {
  const isRunning = !node.finished_at && node.status !== "pending";
  const holder = parseHolder(node.claimed_by);
  const dur = elapsed(node);

  return (
    <div className="border-b border-[var(--border)] shrink-0 px-4 py-2 flex flex-col gap-1">
      <div className="flex items-center gap-3 text-xs flex-wrap">
        <StatusLabel status={node.status} />
        <span className="font-mono text-[var(--foreground)] font-semibold">
          {node.id}
        </span>
        {holder.label && (
          <span className="text-[var(--muted)]">
            on <span className="font-mono text-[#c9d1d9]">{holder.label}</span>
          </span>
        )}
        {dur && <span className="font-mono text-[var(--muted)]">{dur}</span>}
        {isRunning && node.last_heartbeat && (
          <span className="font-mono text-[var(--muted)]">
            <HeartbeatLabel lastHeartbeat={node.last_heartbeat} />
          </span>
        )}
        {node.failure_reason && (
          <span className="ml-auto">
            <FailureReasonBadge
              reason={node.failure_reason}
              exitCode={node.exit_code}
            />
          </span>
        )}
      </div>
      {node.status_detail && (
        <div className="text-xs text-[var(--muted)] font-mono truncate">
          ↳ {node.status_detail}
        </div>
      )}
      {node.error && !node.failure_reason && (
        <div
          className="text-xs text-red-400 font-mono truncate"
          title={node.error}
        >
          {node.error}
        </div>
      )}
    </div>
  );
}
