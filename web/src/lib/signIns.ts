import { authFetch } from "./api";
import { failure, send } from "./teams";

export interface LinkedSignIn {
  provider: string;
  email: string;
  created_at: number;
}

export interface SignIns {
  identities: LinkedSignIn[];
  // The providers this deployment offers, which are the ones an account can
  // link.
  providers: string[];
}

const providerLabels: Record<string, string> = {
  google: "Google",
  github: "GitHub",
};

export function providerLabel(provider: string): string {
  return providerLabels[provider] ?? provider;
}

// A native form, because the dashboard server answers it with a redirect to
// the provider and keeps the flow in a cookie the page never sees.
export function linkFormAction(provider: string): string {
  return `/auth/${encodeURIComponent(provider)}/link`;
}

export const signInsPath = "/account/sign-ins";
export const howAccountsWorkId = "how-accounts-work";

export async function getSignIns(): Promise<SignIns> {
  const res = await send(
    "GET",
    "/api/v1/me/identities",
    "List linked sign-ins",
  );
  const body = (await res.json()) as Partial<SignIns>;
  return {
    identities: body.identities ?? [],
    providers: body.providers ?? [],
  };
}

// Every provider the deployment offers, plus any the account is linked to
// that it no longer offers, so an old sign-in can still be unlinked.
export function signInRows(
  signIns: SignIns,
): { provider: string; linked: LinkedSignIn | null }[] {
  const providers = [...signIns.providers];
  for (const id of signIns.identities) {
    if (!providers.includes(id.provider)) providers.push(id.provider);
  }
  return providers.map((provider) => ({
    provider,
    linked: signIns.identities.find((id) => id.provider === provider) ?? null,
  }));
}

export type UnlinkResult =
  { kind: "unlinked" } | { kind: "last" } | { kind: "reauth" };

// A 409 and a 403 are answers the page explains in place, not failures.
export async function unlinkSignIn(provider: string): Promise<UnlinkResult> {
  const res = await authFetch(
    `/api/v1/me/identities/${encodeURIComponent(provider)}`,
    { method: "DELETE" },
  );
  if (res.status === 409 || res.status === 403) {
    const body = (await res
      .clone()
      .json()
      .catch(() => ({}))) as { error?: { details?: { reason?: string } } };
    const reason = body.error?.details?.reason;
    if (reason === "last_sign_in_method") return { kind: "last" };
    if (reason === "reauth_required") return { kind: "reauth" };
  }
  if (!res.ok) throw await failure(res, `Unlink ${providerLabel(provider)}`);
  return { kind: "unlinked" };
}

export interface LinkNotice {
  tone: "success" | "error";
  message: string;
}

export const reauthMessage =
  "Linking and unlinking need a recent sign-in. Sign out, sign in again, then try within 10 minutes.";

// The controller sends a completed link back with the provider's fixed code,
// and the page words it here, so nothing a URL carries is shown as text. A
// refused link ends on the controller's own error answer.
export function linkNotice(search: string): LinkNotice | null {
  const query = new URLSearchParams(search);
  const linked = providerLabels[query.get("linked") ?? ""];
  if (!linked) return null;
  return {
    tone: "success",
    message: `${linked} is now a way to sign in to this account.`,
  };
}
