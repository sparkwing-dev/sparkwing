export default function StatusLabel({ status }: { status: string }) {
  const styles: Record<string, { bg: string; text: string; label: string }> = {
    pending: {
      bg: "bg-yellow-500/15",
      text: "text-yellow-400",
      label: "pending",
    },
    claimed: { bg: "bg-blue-500/15", text: "text-blue-400", label: "claimed" },
    running: {
      bg: "bg-indigo-500/15",
      text: "text-indigo-400",
      label: "running",
    },
    complete: {
      bg: "bg-green-500/15",
      text: "text-green-400",
      label: "passed",
    },
    success: { bg: "bg-green-500/15", text: "text-green-400", label: "passed" },
    failed: { bg: "bg-red-500/15", text: "text-red-400", label: "failed" },
    cached: { bg: "bg-cyan-500/15", text: "text-cyan-400", label: "cached" },
    paused: {
      bg: "bg-yellow-500/15",
      text: "text-yellow-400",
      label: "paused",
    },
    cancelled: {
      bg: "bg-gray-500/15",
      text: "text-gray-400",
      label: "cancelled",
    },
    "k8s-fallback": {
      bg: "bg-purple-500/15",
      text: "text-purple-400",
      label: "k8s",
    },
  };

  const s = styles[status] || {
    bg: "bg-gray-500/15",
    text: "text-gray-400",
    label: status,
  };

  return (
    <span
      className={`inline-block px-2 py-0.5 rounded text-xs font-mono ${s.bg} ${s.text}`}
    >
      {s.label}
    </span>
  );
}
