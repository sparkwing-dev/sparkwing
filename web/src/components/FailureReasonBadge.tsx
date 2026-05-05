const reasons: Record<string, { label: string; bg: string; text: string; icon: string }> = {
  oom_killed:    { label: "OOM Killed",    bg: "bg-orange-500/20", text: "text-orange-400", icon: "!!" },
  timeout:       { label: "Timed out",     bg: "bg-yellow-500/20", text: "text-yellow-400", icon: "T" },
  agent_lost:    { label: "Agent lost",    bg: "bg-red-500/15",    text: "text-red-400",    icon: "?" },
  queue_timeout: { label: "Queue timeout", bg: "bg-amber-500/15",  text: "text-amber-400",  icon: "Q" },
  pod_error:     { label: "Pod error",     bg: "bg-red-500/15",    text: "text-red-300",    icon: "X" },
  error:         { label: "Error",         bg: "bg-red-500/15",    text: "text-red-300",    icon: "E" },
};

export default function FailureReasonBadge({ reason, exitCode, compact }: { reason?: string; exitCode?: number; compact?: boolean }) {
  if (!reason) return null;
  const r = reasons[reason] || { label: reason, bg: "bg-red-500/15", text: "text-red-300", icon: "!" };

  if (compact) {
    return (
      <span className={`inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-bold ${r.bg} ${r.text}`} title={`${r.label}${exitCode != null ? ` (exit ${exitCode})` : ""}`}>
        {r.label}
      </span>
    );
  }

  return (
    <span className={`inline-flex items-center gap-1.5 px-2 py-1 rounded text-xs font-mono ${r.bg} ${r.text} border border-current/20`}>
      <span className="font-bold">{r.label}</span>
      {exitCode != null && <span className="opacity-70">exit {exitCode}</span>}
    </span>
  );
}
