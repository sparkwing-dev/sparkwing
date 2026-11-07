const STORAGE_KEY = "sparkwing.localSession";
export const LOCAL_LOGIN_PATH = "/login";
export const LOCAL_LOGOUT_PATH = "/logout";

type BrowserWindow = {
  __SPARKWING_REQUIRE_LOGIN__?: string;
  localStorage?: Storage;
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

// Only a team or Cloud controller serves browser sign-in.
export function signInRequired(): boolean {
  return browser()?.__SPARKWING_REQUIRE_LOGIN__ === "true";
}

export function codeFromFragment(hash: string | undefined): string | null {
  const match = /(?:^#|&)code=([^&]+)/.exec(hash ?? "");
  return match ? decodeURIComponent(match[1]) : null;
}

let exchange: Promise<void> | null = null;
let signInFailure: string | null = null;

// Why the last sign-in link was refused, for the signed-out page to say.
export function localSignInFailure(): string | null {
  return signInFailure;
}

async function refusal(res: Response): Promise<string> {
  try {
    const body = (await res.json()) as { error?: { message?: unknown } };
    if (typeof body.error?.message === "string") return body.error.message;
  } catch {}
  return `the controller refused it (HTTP ${res.status})`;
}

// The sign-in code arrives in the fragment, which the browser never sends to a
// server. It leaves history before the exchange, so neither a reload nor the
// back button can present it again. It returns null when nothing is pending,
// so ordinary requests start without waiting a tick.
export function ensureLocalSignIn(): Promise<void> | null {
  const w = browser();
  if (!w || !localDashboard()) return null;
  if (exchange) return exchange;
  const code = codeFromFragment(w.location?.hash);
  if (!code) return null;
  w.history?.replaceState?.(
    null,
    "",
    `${w.location?.pathname ?? "/"}${w.location?.search ?? ""}`,
  );
  exchange = fetch(LOCAL_LOGIN_PATH, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ code }),
  })
    .then(async (res) => {
      if (!res.ok) {
        signInFailure = await refusal(res);
        return;
      }
      const grant = (await res.json()) as { token?: unknown };
      if (typeof grant.token !== "string" || grant.token === "") {
        signInFailure = "the controller returned no session";
        return;
      }
      const previous = localSession();
      w.localStorage?.setItem(STORAGE_KEY, grant.token);
      signInFailure = null;
      // A replaced session is revoked best-effort; it would otherwise stay valid until it expires.
      if (previous && previous !== grant.token) {
        await fetch(LOCAL_LOGOUT_PATH, {
          method: "POST",
          headers: { Authorization: `Bearer ${previous}` },
        }).catch(() => undefined);
      }
    })
    .catch(() => {
      signInFailure = "the controller did not answer";
    })
    .finally(() => {
      exchange = null;
    });
  return exchange;
}

// localStorage belongs to this exact origin, port included, so another service
// on the same host cannot read the token the way it would receive a cookie.
export function localSession(): string | null {
  const w = browser();
  if (!w || !localDashboard()) return null;
  try {
    return w.localStorage?.getItem(STORAGE_KEY) ?? null;
  } catch {
    return null;
  }
}

export function localSignedIn(): boolean {
  return localSession() !== null;
}

export function localAuthHeaders(): Record<string, string> {
  const session = localSession();
  return session ? { Authorization: `Bearer ${session}` } : {};
}

// The token is forgotten only once the controller has revoked it; a 401 means
// it had already ended. Any other failure leaves it live, so this rejects.
export async function logOutLocal(): Promise<void> {
  const headers = localAuthHeaders();
  if (!headers.Authorization) return;
  const res = await fetch(LOCAL_LOGOUT_PATH, { method: "POST", headers });
  if (!res.ok && res.status !== 401) {
    throw new Error(`sign-out failed (HTTP ${res.status}); the session is still active`);
  }
  try {
    browser()?.localStorage?.removeItem(STORAGE_KEY);
  } catch {}
}
