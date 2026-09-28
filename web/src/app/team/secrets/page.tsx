"use client";

import TeamShell from "@/components/TeamShell";
import SecretsManager from "@/components/SecretsManager";
import { canManageTeam } from "@/lib/teams";

export default function SecretsPage() {
  return (
    <TeamShell>
      {(me) => (
        <SecretsManager
          owner={canManageTeam(me.active_team.role)}
          audience="team"
        />
      )}
    </TeamShell>
  );
}
