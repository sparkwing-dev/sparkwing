// The local dashboard (`sparkwing serve`) admits the account that started it.
// A browser proves that once: `sparkwing serve status` prints a link whose
// fragment carries a single-use sign-in code, which never reaches a server
// log. The page trades that code for a session credential kept in
// localStorage, which is scoped to this origin including its port, and sends
// it as a bearer on every API call.

const STORAGE_KEY = "sparkwing.localSession";
export const SESSION_EXCHANGE_PATH = "/auth/local/session";

type BrowserWindow = {
  localStorage?: Storage;
  location?: { hash?: string; pathname?: string; search?: string };
  history?: { replaceState?: (data: unknown, unused: string, url?: string) => void };
};

function browser(): BrowserWindow | null {
  if (typeof window === "undefined") return null;
  return window as unknown as BrowserWindow;
}

export function localSession(): string | null {
  try {
    return browser()?.localStorage?.getItem(STORAGE_KEY) ?? null;
  } catch {
    return null;
  }
}

export function localAuthHeaders(): Record<string, string> {
  const session = localSession();
  return session ? { Authorization: `Bearer ${session}` } : {};
}

export function signInCodeFrom(hash: string | undefined): string | null {
  const match = /(?:^#|&)code=([^&]+)/.exec(hash ?? "");
  return match ? decodeURIComponent(match[1]) : null;
}

async function adoptSignInCode(): Promise<void> {
  const w = browser();
  const code = signInCodeFrom(w?.location?.hash);
  if (!w || !code) return;
  // The code is spent by the exchange below, so it leaves the address bar
  // and the history entry before anything else can read it.
  w.history?.replaceState?.(null, "", `${w.location?.pathname ?? "/"}${w.location?.search ?? ""}`);
  try {
    const res = await fetch(SESSION_EXCHANGE_PATH, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ code }),
    });
    if (!res.ok) return;
    const body = (await res.json()) as { session?: string };
    if (body.session) w.localStorage?.setItem(STORAGE_KEY, body.session);
  } catch {
    // An unreachable or refused exchange leaves the page signed out, which
    // the next API call reports.
  }
}

let adoption: Promise<void> | null = null;

// signedIn resolves once a sign-in code in the URL, if any, has been traded
// for a session, so the first API call already carries it.
export function signedIn(): Promise<void> {
  adoption ??= adoptSignInCode();
  return adoption;
}
