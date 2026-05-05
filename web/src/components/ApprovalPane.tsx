"use client";

// ApprovalPane renders the "awaiting approval" banner for a node whose
// status is approval_pending. Shows the prompt message, a multiline
// comment textarea, and two buttons. Clicking Approve / Deny hits
// POST /api/v1/runs/{run}/approvals/{node} and the parent page re-
// fetches to pick up the resolution.

import { useEffect, useState } from "react";
import { type Approval, getApproval, resolveApproval } from "@/lib/api";

interface Props {
  runID: string;
  nodeID: string;
  // onResolved fires after a successful resolve so the parent page can
  // trigger its normal run-detail refresh without a full reload.
  onResolved?: (a: Approval) => void;
}

export default function ApprovalPane({ runID, nodeID, onResolved }: Props) {
  const [appr, setAppr] = useState<Approval | null>(null);
  const [comment, setComment] = useState("");
  const [busy, setBusy] = useState<"approve" | "deny" | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setAppr(null);
    setError(null);
    (async () => {
      const a = await getApproval(runID, nodeID);
      if (!cancelled) setAppr(a);
    })();
    return () => {
      cancelled = true;
    };
  }, [runID, nodeID]);

  async function onResolve(resolution: "approved" | "denied") {
    if (busy) return;
    setBusy(resolution === "approved" ? "approve" : "deny");
    setError(null);
    try {
      const updated = await resolveApproval(runID, nodeID, resolution, comment);
      setAppr(updated);
      onResolved?.(updated);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  }

  if (!appr) {
    return (
      <div className="text-sm text-[var(--muted)] p-3">Loading approval...</div>
    );
  }

  if (appr.resolved_at) {
    return (
      <div className="border border-[var(--border)] rounded-sm p-3 mb-3 text-sm">
        <div className="font-medium mb-1">
          {appr.resolution === "approved"
            ? "Approved"
            : appr.resolution === "denied"
              ? "Denied"
              : "Timed out"}
          {appr.approver ? ` by ${appr.approver}` : ""}
        </div>
        {appr.comment ? (
          <div className="text-[var(--muted)] whitespace-pre-wrap">
            {appr.comment}
          </div>
        ) : null}
      </div>
    );
  }

  return (
    <div className="border border-yellow-500/40 bg-yellow-500/5 rounded-sm p-3 mb-3">
      <div className="text-sm font-medium mb-2 text-yellow-300">
        Awaiting approval
      </div>
      <div className="text-sm mb-3 whitespace-pre-wrap">
        {appr.message || `Approve ${nodeID}?`}
      </div>
      <textarea
        value={comment}
        onChange={(e) => setComment(e.target.value)}
        placeholder="Optional comment"
        rows={2}
        className="w-full text-sm bg-[var(--surface)] border border-[var(--border)] rounded-sm px-2 py-1 mb-2 font-mono"
      />
      {error ? <div className="text-sm text-red-400 mb-2">{error}</div> : null}
      <div className="flex gap-2">
        <button
          type="button"
          disabled={busy !== null}
          onClick={() => onResolve("approved")}
          className="px-3 py-1 text-sm bg-indigo-500/80 hover:bg-indigo-500 text-white rounded-sm disabled:opacity-50"
        >
          {busy === "approve" ? "Approving..." : "Approve"}
        </button>
        <button
          type="button"
          disabled={busy !== null}
          onClick={() => onResolve("denied")}
          className="px-3 py-1 text-sm bg-[var(--surface-raised)] border border-[var(--border)] hover:bg-[var(--surface)] rounded-sm disabled:opacity-50"
        >
          {busy === "deny" ? "Denying..." : "Deny"}
        </button>
      </div>
    </div>
  );
}
