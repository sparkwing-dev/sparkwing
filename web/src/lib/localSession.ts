export const LOCAL_LOGOUT_PATH = "/logout";

type BrowserWindow = {
  __SPARKWING_REQUIRE_LOGIN__?: string;
  document?: { cookie?: string };
};

function browser(): BrowserWindow | null {
  if (typeof window === "undefined") return null;
  return window as unknown as BrowserWindow;
}

export function localDashboard(): boolean {
  return browser()?.__SPARKWING_REQUIRE_LOGIN__ === "false";
}

// Only a team or Cloud controller serves browser sign-in.
export function signInRequired(): boolean {
  return browser()?.__SPARKWING_REQUIRE_LOGIN__ === "true";
}

// The session cookie is HttpOnly; this marker cookie carries no authority and
// only tells the page that a sign-out has something to end.
export function localSignedIn(): boolean {
  if (!localDashboard()) return false;
  const cookie = browser()?.document?.cookie ?? "";
  return cookie.split(";").some((part) => part.trim() === "sw_dashboard_signed_in=1");
}

// The local session is an HttpOnly cookie, so the browser sends it and the
// Origin header the controller requires on this write.
// A 401 means the session had already ended; any other failure leaves it live,
// so it rejects for the caller to say so.
export async function logOutLocal(): Promise<void> {
  const res = await fetch(LOCAL_LOGOUT_PATH, { method: "POST" });
  if (res.ok || res.status === 401) return;
  throw new Error(`sign-out failed (HTTP ${res.status}); the session is still active`);
}
