"use client";

import { useState } from "react";

export default function CopyField({
  value,
  label,
  multiline = false,
}: {
  value: string;
  label: string;
  multiline?: boolean;
}) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  }
  return (
    <div className="flex items-stretch gap-2">
      <code
        aria-label={label}
        className={`flex-1 min-w-0 px-3 py-2 text-xs font-mono rounded-[var(--radius-control)] bg-[var(--background)] border border-[var(--border)] ${
          multiline ? "whitespace-pre-wrap break-all" : "truncate"
        }`}
      >
        {value}
      </code>
      <button
        type="button"
        onClick={copy}
        className="px-3 text-xs rounded-[var(--radius-control)] border border-[var(--border)] text-[var(--muted)] hover:text-[var(--foreground)] hover:bg-[var(--surface-raised)] shrink-0"
      >
        {copied ? "Copied" : "Copy"}
      </button>
    </div>
  );
}
