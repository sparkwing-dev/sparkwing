"use client";

// SelectedNodePanel renders the per-node detail strip -- runner
// identity, current activity, last heartbeat. Pulled out of the
// run-level header (where it used to be jammed into the same 2-col
// grid as run id / commit / branch) so the Setup section stays
// scoped to the run as a whole.

import type { Node as RunNode } from "@/lib/api";
import { parseHolder } from "@/lib/api";
import { HeartbeatLabel } from "@/components/HeartbeatDot";
import FailureReasonBadge from "@/components/FailureReasonBadge";

export default function SelectedNodePanel({ node }: { node: RunNode }) {
  const isRunning = !node.finished_at && node.status !== "pending";
  const holder = parseHolder(node.claimed_by);
  return (
    <div className="border-b border-[var(--border)] shrink-0 px-4 py-2 space-y-1">
      <div className="flex items-baseline gap-3 text-xs">
        <div className="w-20 shrink-0 text-[var(--muted)] font-mono">node</div>
        <div className="font-mono text-[var(--foreground)]">{node.id}</div>
      </div>
      <div className="flex items-baseline gap-3 text-xs">
        <div className="w-20 shrink-0 text-[var(--muted)] font-mono">
          runner
        </div>
        <div className="font-mono text-[var(--foreground)]">{holder.label}</div>
      </div>
      {node.status_detail && (
        <div className="flex items-baseline gap-3 text-xs">
          <div className="w-20 shrink-0 text-[var(--muted)] font-mono">
            activity
          </div>
          <div className="font-mono text-[var(--foreground)]">
            {node.status_detail}
          </div>
        </div>
      )}
      {isRunning && node.last_heartbeat && (
        <div className="flex items-baseline gap-3 text-xs">
          <div className="w-20 shrink-0 text-[var(--muted)] font-mono">
            heartbeat
          </div>
          <div className="font-mono">
            <HeartbeatLabel lastHeartbeat={node.last_heartbeat} />
          </div>
        </div>
      )}
      {node.failure_reason && (
        <div className="pt-1">
          <FailureReasonBadge
            reason={node.failure_reason}
            exitCode={node.exit_code}
          />
        </div>
      )}
    </div>
  );
}
