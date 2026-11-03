// RunRequester names who requested a run and, when a coding agent started it on
// their behalf, the agent beside them. The agent label is self-declared and
// display-only, so it reads as context, never as an identity badge.
export default function RunRequester({
  requester,
  agent,
  className = "",
}: {
  requester?: string;
  agent?: string;
  className?: string;
}) {
  if (!requester && !agent) return null;
  return (
    <span className={`inline-flex items-center gap-1 min-w-0 text-[var(--muted)] ${className}`}>
      {requester && <span className="truncate">by {requester}</span>}
      {agent && (
        <span
          role="img"
          aria-label={`started by an agent: ${agent}`}
          title={`Started by an agent: ${agent}`}
          className="inline-flex items-center gap-0.5 min-w-0"
        >
          <AgentIcon />
          <span aria-hidden="true" className="truncate font-mono">
            {agent}
          </span>
        </span>
      )}
    </span>
  );
}

function AgentIcon() {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 16 16"
      width="11"
      height="11"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      className="shrink-0"
    >
      <rect x="3" y="5" width="10" height="8" rx="2" />
      <path d="M8 5V2.5M6 9h.01M10 9h.01M1.5 9v2M14.5 9v2" strokeLinecap="round" />
    </svg>
  );
}
