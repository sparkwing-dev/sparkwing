"use client";

import { useEffect, useState } from "react";
import {
  DOCS_URL,
  limitHelp,
  readSupportContact,
  type SupportContact,
} from "@/lib/support";

export function HelpPanel({ contact }: { contact: SupportContact | null }) {
  return (
    <div
      role="dialog"
      aria-label="Help"
      className="absolute right-0 mt-1 w-80 p-3 space-y-2 text-sm bg-[var(--surface)] border border-[var(--border)] rounded-sm shadow-lg z-50"
    >
      <p>
        Need higher limits, have feedback, or want to suggest an improvement?
        Reach out, we read everything.
      </p>
      {contact ? (
        <p>
          <a
            href={contact.href}
            className="text-[var(--accent)] hover:underline"
            target={contact.href.startsWith("mailto:") ? undefined : "_blank"}
            rel="noopener noreferrer"
          >
            {contact.label}
          </a>
        </p>
      ) : (
        <p className="text-[var(--muted)]">
          Contact your Sparkwing administrator.
        </p>
      )}
      <p>
        <a
          href={DOCS_URL}
          className="text-[var(--accent)] hover:underline"
          target="_blank"
          rel="noopener noreferrer"
        >
          Read the docs
        </a>
      </p>
    </div>
  );
}

export default function HelpMenu({
  initialOpen = false,
}: {
  initialOpen?: boolean;
}) {
  const [open, setOpen] = useState(initialOpen);
  const [contact, setContact] = useState<SupportContact | null>(null);
  useEffect(() => {
    queueMicrotask(() => setContact(readSupportContact()));
  }, []);
  return (
    <div className="relative ml-2">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="px-2 py-1 text-xs rounded-sm border border-[var(--accent)] text-[var(--foreground)] hover:bg-[var(--surface-raised)]"
      >
        Help
      </button>
      {open ? <HelpPanel contact={contact} /> : null}
    </div>
  );
}

// LimitHelp names who can raise a limit, next to a setting or refusal that hits one.
export function LimitHelp() {
  const [contact, setContact] = useState<SupportContact | null>(null);
  useEffect(() => {
    queueMicrotask(() => setContact(readSupportContact()));
  }, []);
  return <span className="text-[var(--muted)]">{limitHelp(contact)}</span>;
}
