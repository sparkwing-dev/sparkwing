import { authFetch } from "./api";

// The controller draws codes from consonants only, so no code spells a word
// and none of its letters look like a digit.
const codeAlphabet = "BCDFGHJKLMNPQRSTVWXZ";
const codeLength = 8;

export const expiredCodeText =
  "That code is not valid or has expired. Run the command again in your terminal for a new code.";

export interface DeviceRunner {
  allowed_labels: string[];
  name: string;
  personal: boolean;
  repos: string[];
  reserved_pools: string[];
  trust: string;
  platform: string;
  launcher: boolean;
}

export interface DeviceRequest {
  user_code: string;
  kind: "member" | "runner";
  machine: string;
  team: string;
  grants: string[];
  runner?: DeviceRunner;
  created_at: string;
  expires_at: string;
}

// Keeps the letters a person typed, in capitals, whatever else they typed
// around them, so a pasted "bcdf ghjk" and "BCDF-GHJK" read the same.
export function normalizeDeviceCode(input: string): string {
  return input
    .toUpperCase()
    .replace(/[^A-Z]/g, "")
    .slice(0, codeLength);
}

export function formatDeviceCode(letters: string): string {
  return letters.length > 4
    ? `${letters.slice(0, 4)}-${letters.slice(4)}`
    : letters;
}

export function deviceCodeComplete(letters: string): boolean {
  return (
    letters.length === codeLength &&
    [...letters].every((letter) => codeAlphabet.includes(letter))
  );
}

// A letter no code uses, such as a vowel, is shown back with a hint rather
// than dropped, so the field never disagrees with what the terminal printed.
export function deviceCodeHasForeignLetter(letters: string): boolean {
  return [...letters].some((letter) => !codeAlphabet.includes(letter));
}

const grantTexts: Record<string, string> = {
  "runs.read": "See runs",
  "logs.read": "Read logs",
  "runs.write": "Start runs",
  "runs.control": "Cancel and retry runs",
  "approvals.write": "Approve or deny run approvals",
  "nodes.claim": "Run jobs",
  "triggers.claim": "Plan runs",
  "claims.launch": "Launch Cloud jobs for a reserved pool",
};

// A grant the console does not know yet is shown by its name, so a newer
// controller never hides what it is asking for.
export function grantText(grant: string): string {
  return Object.hasOwn(grantTexts, grant) ? grantTexts[grant] : grant;
}

export function minutesLeft(expiresAt: string, now: number): number {
  const at = Date.parse(expiresAt);
  if (Number.isNaN(at)) return 0;
  return Math.max(0, Math.ceil((at - now) / 60_000));
}

export function expiryText(expiresAt: string, now: number): string {
  const minutes = minutesLeft(expiresAt, now);
  if (minutes === 0) return "This request has expired.";
  return minutes === 1
    ? "This request expires in about 1 minute."
    : `This request expires in about ${minutes} minutes.`;
}

export class DeviceLoginError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

async function refusal(
  res: Response,
  action: string,
): Promise<DeviceLoginError> {
  let code = "";
  let message = "";
  try {
    const body = (await res.json()) as {
      error?: { code?: unknown; message?: unknown };
    };
    if (typeof body.error?.code === "string") code = body.error.code;
    if (typeof body.error?.message === "string") message = body.error.message;
  } catch {
    // A body that is not the error envelope falls back to the status below.
  }
  // An unknown, expired and already decided code all answer not_found, so
  // the page cannot tell anyone which codes exist.
  if (res.status === 404) {
    return new DeviceLoginError(404, code || "not_found", expiredCodeText);
  }
  if ((res.status === 429 || res.status === 403) && message) {
    return new DeviceLoginError(res.status, code, message);
  }
  return new DeviceLoginError(
    res.status,
    code,
    message ? `${action}: ${message}` : `${action} failed (${res.status})`,
  );
}

async function send(
  method: "POST",
  path: string,
  letters: string,
  action: string,
): Promise<Response> {
  const res = await authFetch(path, {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ user_code: letters }),
  });
  if (!res.ok) throw await refusal(res, action);
  return res;
}

export async function lookupDeviceRequest(
  letters: string,
): Promise<DeviceRequest> {
  const res = await send(
    "POST",
    "/api/v1/auth/device/lookup",
    letters,
    "Look up the code",
  );
  const body = (await res.json()) as Partial<DeviceRequest>;
  if (body.kind === "runner" && (
    !body.runner || !Array.isArray(body.runner.allowed_labels) ||
    body.runner.allowed_labels.some((label) => typeof label !== "string")
  )) {
    throw new DeviceLoginError(502, "protocol_error", "The controller did not return the runner's allowed labels. Try the enrollment again before approving.");
  }
  return {
    user_code: body.user_code ?? letters,
    kind: body.kind === "runner" ? "runner" : "member",
    machine: body.machine ?? "",
    team: body.team ?? "",
    grants: body.grants ?? [],
    runner: body.runner
      ? {
          ...body.runner,
          allowed_labels: body.runner.allowed_labels,
          repos: body.runner.repos ?? [],
          reserved_pools: body.runner.reserved_pools ?? [],
        }
      : undefined,
    created_at: body.created_at ?? "",
    expires_at: body.expires_at ?? "",
  };
}

export async function approveDeviceRequest(letters: string): Promise<void> {
  await send("POST", "/api/v1/auth/device/approve", letters, "Approve");
}

export async function denyDeviceRequest(letters: string): Promise<void> {
  await send("POST", "/api/v1/auth/device/deny", letters, "Deny");
}
