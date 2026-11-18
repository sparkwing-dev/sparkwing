import { authFetch } from "./api";

export const resealCounts = [
  ["secrets", "Secrets"],
  ["git_credentials", "Git credentials"],
  ["run_environments", "Run environments"],
  ["run_arguments", "Sealed argument documents"],
  ["plaintext_run_arguments", "Plaintext argument documents"],
  ["settings", "Settings"],
  ["run_event_subscriptions", "Event subscription secrets"],
  ["previous_key_only", "Previous key only"],
  ["unreadable", "Unreadable rows"],
] as const;

export type ResealCounts = Record<(typeof resealCounts)[number][0], number>;

function countsFrom(body: unknown): ResealCounts {
  if (typeof body !== "object" || body === null) throw new Error("Incomplete verification counts. Run Verify secrets again.");
  const values = body as Record<string, unknown>;
  const result = {} as ResealCounts;
  for (const [key] of resealCounts) {
    const value = key === "unreadable"
      ? values.unreadable === null ? 0 : Array.isArray(values.unreadable) ? values.unreadable.length : undefined
      : values[key];
    if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) {
      throw new Error("Incomplete verification counts. Run Verify secrets again.");
    }
    result[key] = value;
  }
  return result;
}

export function resealVerified(counts: ResealCounts): boolean {
  return counts.unreadable === 0 && counts.previous_key_only === 0 && counts.plaintext_run_arguments === 0;
}

export class ResealRequestError extends Error {
  constructor(message: string, readonly status: number | null) {
    super(message);
  }
}

export async function requestReseal(verify: boolean): Promise<ResealCounts> {
  let response: Response;
  try {
    response = await authFetch(
      `/api/v1/operator/secrets/rotate${verify ? "?verify=1" : ""}`,
      { method: "POST", credentials: "same-origin", redirect: "error" },
      { speaksForSession: false, timeoutMs: null },
    );
  } catch {
    throw new ResealRequestError("The request did not finish. Its outcome is unknown. Run Verify secrets before trying again.", null);
  }
  const body = await response.json().catch(() => null);
  if (!response.ok) {
    if (response.status === 401 || body?.error?.details?.reason === "reauth_required") {
      throw new ResealRequestError("Sign in again, then run Verify secrets within 10 minutes.", response.status);
    }
    if (response.status === 403) throw new ResealRequestError("This action requires an operator console session and valid CSRF proof. Sign in again, then run Verify secrets.", response.status);
    if (body?.error?.details?.reason === "secrets_key_required") {
      throw new ResealRequestError("Secret encryption is unavailable. Check the controller key configuration, then run Verify secrets before retrying.", response.status);
    }
    throw new ResealRequestError(`The controller could not complete the request (HTTP ${response.status}). Run Verify secrets before trying again.`, response.status);
  }
  return countsFrom(body);
}
