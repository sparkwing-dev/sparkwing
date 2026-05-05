"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";
import { type Approval, getPendingApprovals } from "@/lib/api";

type Tab = { href: string; label: string; external?: boolean };

const tabs: Tab[] = [
  { href: "/", label: "Home" },
  { href: "/runs", label: "Runs" },
  { href: "/pipeline-overview", label: "Pipeline Overview" },
  { href: "/agents", label: "Agents" },
  { href: "/cluster", label: "Cluster" },
  { href: "https://sparkwing.dev/docs/", label: "Docs", external: true },
];

// Polling cadence for the pending-approvals badge. 10s is a compromise
// between "badge feels live" and "don't thrash the controller while
// dashboards are left open in a tab all day".
const APPROVALS_POLL_MS = 10_000;

export default function Nav() {
  const pathname = usePathname();
  const [pending, setPending] = useState<Approval[]>([]);
  const [open, setOpen] = useState(false);

  useEffect(() => {
    let cancelled = false;
    async function tick() {
      const rows = await getPendingApprovals();
      if (!cancelled) setPending(rows);
    }
    tick();
    const t = setInterval(tick, APPROVALS_POLL_MS);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, []);

  return (
    <div className="flex items-center gap-1 px-4 border-b border-[var(--border)] bg-[var(--surface)]">
      <Link href="/" className="text-lg font-bold mr-4 py-2">
        sparkwing
      </Link>
      <div className="flex items-center gap-1 flex-1">
        {tabs.map((tab) => {
          const active = tab.external
            ? false
            : tab.href === "/"
              ? pathname === "/"
              : pathname.startsWith(tab.href);
          const className = `px-3 py-2 text-sm border-b-2 transition-colors ${
            active
              ? "border-[var(--accent)] text-[var(--foreground)]"
              : "border-transparent text-[var(--muted)] hover:text-[var(--foreground)]"
          }`;
          if (tab.external) {
            return (
              <a
                key={tab.href}
                href={tab.href}
                target="_blank"
                rel="noopener noreferrer"
                className={className}
              >
                {tab.label}
              </a>
            );
          }
          return (
            <Link key={tab.href} href={tab.href} className={className}>
              {tab.label}
            </Link>
          );
        })}
      </div>
      <ApprovalsBadge
        pending={pending}
        open={open}
        onToggle={() => setOpen((v) => !v)}
        onClose={() => setOpen(false)}
      />
    </div>
  );
}

function ApprovalsBadge({
  pending,
  open,
  onToggle,
  onClose,
}: {
  pending: Approval[];
  open: boolean;
  onToggle: () => void;
  onClose: () => void;
}) {
  if (pending.length === 0 && !open) {
    return null;
  }
  return (
    <div className="relative">
      <button
        type="button"
        onClick={onToggle}
        className="px-2 py-1 text-xs rounded-sm bg-indigo-500/20 text-indigo-300 hover:bg-indigo-500/30"
        title="Pending approvals"
      >
        approvals · {pending.length}
      </button>
      {open ? (
        <div
          className="absolute right-0 mt-1 w-80 bg-[var(--surface)] border border-[var(--border)] rounded-sm shadow-lg z-50"
          onMouseLeave={onClose}
        >
          {pending.length === 0 ? (
            <div className="p-3 text-sm text-[var(--muted)]">
              No pending approvals.
            </div>
          ) : (
            <ul>
              {pending.map((a) => (
                <li
                  key={`${a.run_id}/${a.node_id}`}
                  className="border-b border-[var(--border)] last:border-0"
                >
                  <Link
                    href={`/runs?run=${encodeURIComponent(a.run_id)}&node=${encodeURIComponent(a.node_id)}`}
                    className="block p-2 text-sm hover:bg-[var(--surface-raised)]"
                    onClick={onClose}
                  >
                    <div className="font-medium">{a.node_id}</div>
                    <div className="text-xs text-[var(--muted)]">
                      {a.run_id}
                    </div>
                    {a.message ? (
                      <div className="text-xs mt-1 truncate">{a.message}</div>
                    ) : null}
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </div>
      ) : null}
    </div>
  );
}
