"use client";

import type { ReactNode } from "react";
import { WaitlistNotice } from "@/components/TeamShell";
import { useTeamState } from "@/lib/useTeam";

// A waitlisted account belongs to no team, so every team view would answer
// 403; it sees the waitlist in their place on every route.
export default function WaitlistGate({ children }: { children: ReactNode }) {
  const state = useTeamState();
  if (state.status === "waitlisted") return <WaitlistNotice me={state.me} />;
  return children;
}
