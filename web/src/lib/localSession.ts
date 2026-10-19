const STORAGE_KEY = "sparkwing.localSession";
export const LOCAL_LOGOUT_PATH = "/logout";

type BrowserWindow = {
  __SPARKWING_REQUIRE_LOGIN__?: string;
  sessionStorage?: Storage;
  location?: { hash?: string; pathname?: string; search?: string };
  history?: {
    replaceState?: (data: unknown, unused: string, url?: string) => void;
  };
};

function browser(): BrowserWindow | null {
  if (typeof window === "undefined") return null;
  return window as unknown as BrowserWindow;
}

export function localDashboard(): boolean {
  return browser()?.__SPARKWING_REQUIRE_LOGIN__ === "false";
}

export function sessionFromFragment(hash: string | undefined): string | null {
  const match = /(?:^#|&)s=([^&]+)/.exec(hash ?? "");
  return match ? decodeURIComponent(match[1]) : null;
}

function adoptFragment(w: BrowserWindow) {
  const session = sessionFromFragment(w.location?.hash);
  if (!session) return;
  w.history?.replaceState?.(
    null,
    "",
    `${w.location?.pathname ?? "/"}${w.location?.search ?? ""}`,
  );
  try {
    w.sessionStorage?.setItem(STORAGE_KEY, session);
  } catch {}
}

export function localSession(): string | null {
  const w = browser();
  if (!w || !localDashboard()) return null;
  adoptFragment(w);
  try {
    return w.sessionStorage?.getItem(STORAGE_KEY) ?? null;
  } catch {
    return null;
  }
}

export function localAuthHeaders(): Record<string, string> {
  const session = localSession();
  return session ? { Authorization: `Bearer ${session}` } : {};
}

export async function logOutLocal(): Promise<void> {
  const headers = localAuthHeaders();
  try {
    browser()?.sessionStorage?.removeItem(STORAGE_KEY);
  } catch {}
  if (!headers.Authorization) return;
  await fetch(LOCAL_LOGOUT_PATH, { method: "POST", headers }).catch(
    () => undefined,
  );
}
