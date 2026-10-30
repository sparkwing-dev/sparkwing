import { listPages } from "./listPages";
import { asList, send } from "./teams";

// One stored row as GET /api/v1/secrets lists it. The controller fills
// `value` for unmasked rows only, so a secret's value never reaches here.
export interface StoredSecret {
  name: string;
  value?: string;
  principal: string;
  // A pipeline row belongs to one repository: repo is its name when the row
  // was written and repo_key the identity a run's repository must match.
  repo?: string;
  repo_key?: string;
  pipeline?: string;
  masked: boolean;
  shared?: boolean;
  only_on?: string;
  bound: boolean;
  created_at: number;
  updated_at: number;
}

export type ScopeKind = "team" | "pipeline";

export interface SecretDraft {
  name: string;
  value: string;
  masked: boolean;
  scope: ScopeKind;
  repo: string;
  pipeline: string;
}

export interface SecretWriteBody {
  name: string;
  value: string;
  masked: boolean;
  repo?: string;
  pipeline?: string;
  shared?: boolean;
  only_on?: string;
}

// Local mode drops team wording because its built-in team is this machine.
export type SecretsAudience = "team" | "local";

export const scopeHelp: Record<SecretsAudience, string> = {
  team: "A pipeline secret belongs to one GitHub repository and one pipeline, like a GitHub repository secret: only runs whose code Sparkwing fetched from that repository, or a GitHub Actions job of that repository, read it, never uploaded or local runs. Team-wide rows answer every pipeline of every repository in the team, like a GitHub organization secret with no repository list.",
  local:
    "Stored in this machine's local sparkwing database. Runs started here read only team-wide rows; a pipeline row reaches only runs whose code Sparkwing fetched from its repository.",
};

// An unscoped row that is not shared answers no run; the CLI can still write
// one, so the page names it rather than calling it team-wide.
export function scopeLabel(
  row: Pick<StoredSecret, "repo" | "pipeline" | "shared">,
  audience: SecretsAudience = "team",
) {
  if (row.pipeline) return `Pipeline: ${row.pipeline} in ${row.repo ?? ""}`;
  if (!row.shared) return "No runs (operator reads only)";
  return audience === "local" ? "Every pipeline" : "Team";
}

export function rowKey(
  row: Pick<StoredSecret, "name" | "repo_key" | "pipeline">,
): string {
  return `${row.name}\u0000${row.repo_key ?? ""}\u0000${row.pipeline ?? ""}`;
}

export function splitSecrets(rows: StoredSecret[]): {
  secrets: StoredSecret[];
  variables: StoredSecret[];
} {
  const sorted = [...rows].sort(
    (a, b) =>
      a.name.localeCompare(b.name) ||
      (a.repo ?? "").localeCompare(b.repo ?? "") ||
      (a.pipeline ?? "").localeCompare(b.pipeline ?? ""),
  );
  return {
    secrets: sorted.filter((r) => r.masked),
    variables: sorted.filter((r) => !r.masked),
  };
}

const nameCharset = /^[A-Za-z0-9._/-]+$/;

// Mirrors pkg/secretname.Validate so the form refuses what the
// controller would; the controller stays the authority.
export function secretNameProblem(name: string): string | null {
  if (name === "") return "Enter a name.";
  if (name.length > 256) return "Use at most 256 characters.";
  if (!nameCharset.test(name)) {
    return "Use letters, digits, and . _ / - only.";
  }
  if (name.includes("..")) return "Do not use '..'.";
  if (name.includes("//")) return "Do not use an empty '/' segment.";
  if (/^[./-]/.test(name)) return "Do not start with '.', '/' or '-'.";
  if (/[./-]$/.test(name)) return "Do not end with '.', '/' or '-'.";
  return null;
}

export function draftProblem(draft: SecretDraft): string | null {
  const nameProblem = secretNameProblem(draft.name);
  if (nameProblem) return nameProblem;
  if (draft.scope === "pipeline" && draft.repo.trim() === "") {
    return "Enter the repository this pipeline belongs to, as owner/name.";
  }
  if (draft.scope === "pipeline" && draft.pipeline.trim() === "") {
    return "Enter the pipeline this row is scoped to.";
  }
  return null;
}

export function draftBody(draft: SecretDraft): SecretWriteBody {
  if (draft.scope === "pipeline") {
    return {
      name: draft.name,
      value: draft.value,
      masked: draft.masked,
      repo: draft.repo.trim(),
      pipeline: draft.pipeline.trim(),
    };
  }
  return {
    name: draft.name,
    value: draft.value,
    masked: draft.masked,
    shared: true,
  };
}

// An update rewrites one existing row's value under its scope, sharing and
// kind. It never names a pool: the controller keeps the row's current pool,
// so a page loaded before a pool change cannot restore the old one.
export function updateBody(row: StoredSecret, value: string): SecretWriteBody {
  const body: SecretWriteBody = { name: row.name, value, masked: row.masked };
  if (row.pipeline) {
    body.repo = row.repo_key;
    body.pipeline = row.pipeline;
  }
  if (row.shared) body.shared = true;
  return body;
}

export function existingRow(
  rows: StoredSecret[],
  draft: SecretDraft,
): StoredSecret | undefined {
  const pipeline = draft.scope === "pipeline" ? draft.pipeline.trim() : "";
  const repo =
    draft.scope === "pipeline" ? draft.repo.trim().toLowerCase() : "";
  return rows.find(
    (r) =>
      r.name === draft.name &&
      (r.pipeline ?? "") === pipeline &&
      (repo === "" ||
        (r.repo ?? "").toLowerCase() === repo ||
        r.repo_key === draft.repo.trim()),
  );
}

export async function listSecrets(): Promise<StoredSecret[]> {
  const res = await listPages("/api/v1/secrets", ["secrets"], (path) => send("GET", path, "List secrets"));
  return asList<StoredSecret>(await res.json(), "secrets");
}

export async function writeSecret(
  body: SecretWriteBody,
): Promise<{ warning?: string }> {
  const res = await send("POST", "/api/v1/secrets", `Save ${body.name}`, body);
  return res.json();
}

export async function deleteSecret(
  row: Pick<StoredSecret, "name" | "repo_key" | "pipeline">,
): Promise<void> {
  const query = row.pipeline
    ? `?repo=${encodeURIComponent(row.repo_key ?? "")}&pipeline=${encodeURIComponent(row.pipeline)}`
    : "";
  await send(
    "DELETE",
    `/api/v1/secrets/${encodeURIComponent(row.name)}${query}`,
    `Delete ${row.name}`,
  );
}
