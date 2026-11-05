"use client";

import type { ReactNode } from "react";
import { usePathname } from "next/navigation";
import { FirstTeamNotice, Notice, WaitlistNotice } from "@/components/TeamShell";
import { useTeamState } from "@/lib/useTeam";

// Operator routes have their own authority checks and need no customer team.
export default function WaitlistGate({ children }: { children: ReactNode }) {
  const state = useTeamState();
  const pathname = usePathname();
  if (pathname === "/operator" || pathname.startsWith("/operator/")) return children;
  if (state.status === "loading") return <Notice>Loading…</Notice>;
  if (state.status === "waitlisted") return <WaitlistNotice me={state.me} />;
  if (state.status === "no-team" && pathname !== "/team/new") {
    return <FirstTeamNotice me={state.me} />;
  }
  return children;
}
