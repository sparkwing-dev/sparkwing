// Switching teams reloads the page so every view reads the new team, which
// would drop a toast shown before it; the note rides sessionStorage across
// that one reload and is read once.
const key = "sparkwing.team-notice";

export function switchedNotice(team: string): string {
  return `Switched to ${team}. Your other teams are still in the team menu.`;
}

export function joinedNotice(team: string): string {
  return `Joined ${team} and switched to it. Your other teams are still in the team menu.`;
}

export function createdNotice(team: string): string {
  return `Created ${team} and switched to it. Your other teams are still in the team menu.`;
}

export function rememberTeamNotice(message: string): void {
  try {
    window.sessionStorage.setItem(key, message);
  } catch {
    // The confirmation is a courtesy; a browser that refuses storage just skips it.
  }
}

export function takeTeamNotice(): string {
  try {
    const message = window.sessionStorage.getItem(key) ?? "";
    window.sessionStorage.removeItem(key);
    return message;
  } catch {
    return "";
  }
}
