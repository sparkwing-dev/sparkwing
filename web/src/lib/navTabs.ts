import { signInRequired } from "./localSession";
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

// Queue and Capacity read the admission daemon on the controller's own
// machine, which only the local dashboard has; a self-hosted single-team
// controller serves sign-in and has none.
export function showsLocalAdmission(status: TeamState["status"]): boolean {
  return status === "single-team" && !signInRequired();
}

const localAdmissionTabs = new Set(["/queue", "/capacity"]);

// A controller without teams, such as the one `sparkwing web` starts, has
// no account to hang team pages on, so Secrets sits in the top nav there.
// A waitlisted account belongs to no team, so every team view would refuse it.
export function navTabs(status: TeamState["status"]): NavTab[] {
  if (status === "waitlisted") return [];
  if (status === "single-team") {
    if (showsLocalAdmission(status)) return localTabs;
    return localTabs.filter((tab) => !localAdmissionTabs.has(tab.href));
  }
  if (status === "ready") return [...cloudTabs, teamTab];
  return cloudTabs;
}
