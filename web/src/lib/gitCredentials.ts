import { asList, send } from "./teams";

export type GitCredentialKind = "ssh" | "https";

// One credential as GET /api/v1/team/git-credentials lists it. The controller
// never returns the private key or token.
export interface GitCredential {
  host: string;
  kind: GitCredentialKind;
  username?: string;
  fingerprint?: string;
  host_key?: string;
  confirmed: boolean;
  created_by: string;
  created_at: number;
  updated_at: number;
}

export interface StoredGitCredential extends GitCredential {
  public_key?: string;
}

export interface GitCredentialRelease {
  host: string;
  run_id: string;
  runner: string;
  token_prefix: string;
  released_at: number;
}

export interface GitCredentialDraft {
  host: string;
  kind: GitCredentialKind;
  privateKey: string;
  port: string;
  token: string;
  username: string;
}

export type GitCredentialWriteBody =
  | { host: string; kind: "ssh"; private_key: string; port?: number }
  | { host: string; kind: "https"; token: string; username?: string };

export const emptyDraft: GitCredentialDraft = {
  host: "",
  kind: "ssh",
  privateKey: "",
  port: "",
  token: "",
  username: "",
};

export const defaultHTTPSUsername = "x-access-token";

export const recipientsHelp =
  "Cloud runners receive a team's git credentials, and so do machines an owner opts in on the Machines page.";

export function normalizeHost(host: string): string {
  return host.trim().toLowerCase();
}

export function isGitHubHost(host: string): boolean {
  return normalizeHost(host) === "github.com";
}

// Mirrors the controller's validGitCredentialHost; the controller stays the
// authority.
export function hostProblem(input: string): string | null {
  const host = normalizeHost(input);
  if (host === "") return "Enter a host.";
  if (/[:/@]/.test(host)) {
    return "Enter a hostname such as gitlab.com, with no scheme, port or path.";
  }
  if (
    host.length > 253 ||
    !host.includes(".") ||
    /^[-.]/.test(host) ||
    host.endsWith(".") ||
    !/^[a-z0-9.-]+$/.test(host)
  ) {
    return "Enter a hostname such as gitlab.com.";
  }
  return null;
}

export function portProblem(port: string): string | null {
  const p = port.trim();
  if (p === "") return null;
  if (!/^\d+$/.test(p) || Number(p) < 1 || Number(p) > 65535) {
    return "Use a port between 1 and 65535, or leave it empty for 22.";
  }
  return null;
}

const printable = /^[!-~]+$/;

export function draftProblem(draft: GitCredentialDraft): string | null {
  const host = hostProblem(draft.host);
  if (host) return host;
  if (draft.kind === "ssh") {
    if (!draft.privateKey.includes("PRIVATE KEY")) {
      return "Paste an unencrypted PEM or OpenSSH private key.";
    }
    return portProblem(draft.port);
  }
  const username = draft.username.trim();
  if (username !== "" && (username.length > 128 || !printable.test(username))) {
    return "Use a username of up to 128 printable characters with no spaces.";
  }
  if (draft.token === "") return "Enter the token.";
  if (draft.token.length > 1024 || !printable.test(draft.token)) {
    return "Use a token of up to 1024 printable characters with no spaces.";
  }
  return null;
}

export function draftBody(draft: GitCredentialDraft): GitCredentialWriteBody {
  const host = normalizeHost(draft.host);
  if (draft.kind === "ssh") {
    const port = draft.port.trim();
    return port === ""
      ? { host, kind: "ssh", private_key: draft.privateKey }
      : {
          host,
          kind: "ssh",
          private_key: draft.privateKey,
          port: Number(port),
        };
  }
  const username = draft.username.trim();
  return username === ""
    ? { host, kind: "https", token: draft.token }
    : { host, kind: "https", token: draft.token, username };
}

// A known_hosts line names a non-default port as "[host]:port", so replacing
// a key reads the host key from the same port it was first read from.
export function portFromHostKey(hostKey: string | undefined): string {
  const m = /^\[[^\]]+\]:(\d+)\s/.exec(hostKey ?? "");
  return m ? m[1] : "";
}

// The draft an Update opens with: the row's host, kind and username, and
// empty secret fields, since the stored values are never shown.
export function updateDraft(row: GitCredential): GitCredentialDraft {
  return {
    ...emptyDraft,
    host: row.host,
    kind: row.kind,
    port: row.kind === "ssh" ? portFromHostKey(row.host_key) : "",
    username: row.kind === "https" ? (row.username ?? "") : "",
  };
}

export function needsConfirmation(row: GitCredential): boolean {
  return row.kind === "ssh" && !row.confirmed;
}

export function statusLabel(row: GitCredential): string {
  if (row.kind === "https") return "Ready";
  return row.confirmed ? "Host key confirmed" : "Host key not confirmed";
}

export function sortCredentials(rows: GitCredential[]): GitCredential[] {
  return [...rows].sort((a, b) => a.host.localeCompare(b.host));
}

export function existingCredential(
  rows: GitCredential[],
  host: string,
): GitCredential | undefined {
  const h = normalizeHost(host);
  return rows.find((r) => r.host === h);
}

const seg = encodeURIComponent;

export async function listGitCredentials(): Promise<GitCredential[]> {
  const res = await send(
    "GET",
    "/api/v1/team/git-credentials",
    "List git credentials",
  );
  return asList<GitCredential>(await res.json(), "credentials");
}

export async function putGitCredential(
  body: GitCredentialWriteBody,
): Promise<StoredGitCredential> {
  const res = await send(
    "POST",
    "/api/v1/team/git-credentials",
    `Save the credential for ${body.host}`,
    body,
  );
  return (await res.json()) as StoredGitCredential;
}

export async function confirmGitCredential(
  host: string,
  fingerprint: string,
): Promise<GitCredential> {
  const res = await send(
    "POST",
    `/api/v1/team/git-credentials/${seg(host)}/confirm`,
    `Confirm the host key of ${host}`,
    { fingerprint },
  );
  return (await res.json()) as GitCredential;
}

export async function deleteGitCredential(host: string): Promise<void> {
  await send(
    "DELETE",
    `/api/v1/team/git-credentials/${seg(host)}`,
    `Delete the credential for ${host}`,
  );
}

export async function listGitCredentialReleases(): Promise<
  GitCredentialRelease[]
> {
  const res = await send(
    "GET",
    "/api/v1/team/git-credentials/releases",
    "List credential releases",
  );
  return asList<GitCredentialRelease>(await res.json(), "releases");
}

export async function setMachineGitCredentials(
  prefix: string,
  enabled: boolean,
): Promise<boolean> {
  const res = await send(
    "PUT",
    `/api/v1/team/runner-tokens/${seg(prefix)}/git-credentials`,
    enabled ? "Send git credentials" : "Stop git credentials",
    { enabled },
  );
  const body = (await res.json()) as { enabled?: boolean };
  return body.enabled === true;
}
