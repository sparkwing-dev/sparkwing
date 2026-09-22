"use client";

import TeamShell, { Panel } from "@/components/TeamShell";

export default function BillingPage() {
  return (
    <TeamShell>
      {(me) => (
        <Panel title="Billing">
          <div className="p-4 text-sm text-[var(--muted)]">
            Plans and credits for {me.active_team.display_name} are coming
            soon.
          </div>
        </Panel>
      )}
    </TeamShell>
  );
}
