"use client";

import { useEffect, useState } from "react";
import {
  type Capabilities,
  type Me,
  type WaitlistedMe,
  getCapabilities,
  getMe,
  teamsEnabled,
} from "./teams";

export type TeamState =
  | { status: "loading" }
  | { status: "single-team" }
  | { status: "ready"; me: Me; caps: Capabilities | null }
  | { status: "waitlisted"; me: WaitlistedMe }
  | { status: "operator" }
  | { status: "unavailable" };

let loaded: Promise<TeamState> | null = null;
const listeners = new Set<(s: TeamState) => void>();

async function readTeamState(): Promise<TeamState> {
  const caps = await getCapabilities();
  if (!teamsEnabled(caps)) return { status: "single-team" };
  const me = await getMe();
  if (me.kind === "member") return { status: "ready", me: me.me, caps };
  if (me.kind === "waitlisted") return { status: "waitlisted", me: me.me };
  return { status: me.kind };
}

// One read per page load is shared by the nav and the page beneath it.
export function loadTeamState(): Promise<TeamState> {
  if (!loaded) loaded = readTeamState();
  return loaded;
}

// Re-reads /me after a change the nav shows, such as a rename or an accepted
// invitation, and hands the result to every mounted reader.
export async function refreshTeamState(): Promise<TeamState> {
  loaded = readTeamState();
  const next = await loaded;
  for (const fn of listeners) fn(next);
  return next;
}

export function useTeamState(): TeamState {
  const [state, setState] = useState<TeamState>({ status: "loading" });
  useEffect(() => {
    let cancelled = false;
    const listener = (s: TeamState) => {
      if (!cancelled) setState(s);
    };
    listeners.add(listener);
    loadTeamState().then(listener);
    return () => {
      cancelled = true;
      listeners.delete(listener);
    };
  }, []);
  return state;
}
