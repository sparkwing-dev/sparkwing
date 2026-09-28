import type { TeamState } from "./useTeam";

export type NavTab = { href: string; label: string; external?: boolean };

const localTabs: NavTab[] = [
  { href: "/", label: "Home" },
  { href: "/runs", label: "Runs" },
  { href: "/queue", label: "Queue" },
  { href: "/crons", label: "Crons" },
  { href: "/capacity", label: "Capacity" },
  { href: "/cluster", label: "Fleet" },
  { href: "/secrets", label: "Secrets" },
  { href: "/analytics", label: "Analytics (preview)" },
  { href: "https://sparkwing.dev/docs/", label: "Docs", external: true },
];

const teamTab: NavTab = { href: "/team", label: "Team" };
const cloudTabs: NavTab[] = [
  { href: "/", label: "Home" },
  { href: "/runs", label: "Runs" },
  { href: "/cluster", label: "Fleet" },
  { href: "/crons", label: "Crons" },
];

// A controller without teams, such as the one `sparkwing web` starts, has
// no account to hang team pages on, so Secrets sits in the top nav there.
export function navTabs(status: TeamState["status"]): NavTab[] {
  if (status === "single-team") return localTabs;
  if (status === "ready") return [...cloudTabs, teamTab];
  return cloudTabs;
}
