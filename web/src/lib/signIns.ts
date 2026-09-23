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
      .catch(() => ({}))) as { error?: string };
    if (body.error === "last_sign_in_method") return { kind: "last" };
    if (body.error === "reauth_required") return { kind: "reauth" };
  }
  if (!res.ok) throw await failure(res, `Unlink ${providerLabel(provider)}`);
  return { kind: "unlinked" };
}

export interface LinkNotice {
  tone: "success" | "error";
  message: string;
  // True when the page should point at the explanation of how accounts
  // work, because the refusal is about another account.
  explain: boolean;
}

export const reauthMessage =
  "Linking and unlinking need a recent sign-in. Sign out, sign in again, then try within 10 minutes.";

// The dashboard server hands a link's result back as a fixed code, and the
// page words it here, so nothing a URL carries is shown as text.
export function linkNotice(search: string): LinkNotice | null {
  const query = new URLSearchParams(search);
  const linked = providerLabels[query.get("linked") ?? ""];
  if (linked) {
    return {
      tone: "success",
      message: `${linked} is now a way to sign in to this account.`,
      explain: false,
    };
  }
  const refused = query.get("refused");
  if (!refused) return null;
  const error = (message: string, explain = false): LinkNotice => ({
    tone: "error",
    message,
    explain,
  });
  const label = providerLabels[query.get("provider") ?? ""];
  if (!label) return error("That sign-in wasn't linked. Try again.");
  switch (refused) {
    case "identity_linked_elsewhere":
      return error(
        `That ${label} sign-in is already linked to another Sparkwing account, so nothing changed. Linking can't move it or anything else from that account to this one.`,
        true,
      );
    case "identity_already_linked":
      return error(`That ${label} sign-in is already linked to this account.`);
    case "provider_already_linked":
      return error(
        `This account already has a ${label} sign-in. Unlink it before linking a different one.`,
      );
    case "reauth_required":
      return error(reauthMessage);
    case "rate_limited":
      return error("Too many link attempts. Wait a minute and try again.");
    case "link_state_invalid":
      return error(
        "The link expired or was started in another tab or session. Start again.",
      );
    case "provider_unverified":
      return error(
        `${label} hasn't verified the email address on that account. Verify it with ${label}, then try again.`,
      );
    case "provider_rejected":
      return error(`${label} didn't confirm the sign-in. Start again.`);
    case "provider_unreachable":
      return error(`${label} couldn't be reached. Try again.`);
    default:
      return error(`${label} wasn't linked. Try again.`);
  }
}
