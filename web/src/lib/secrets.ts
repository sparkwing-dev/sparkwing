import { asList, send } from "./teams";

// One stored row as GET /api/v1/secrets lists it. The controller fills
// `value` for unmasked rows only, so a secret's value never reaches here.
export interface StoredSecret {
  name: string;
  value?: string;
  principal: string;
  pipeline?: string;
  masked: boolean;
  shared?: boolean;
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
  pipeline: string;
}

export interface SecretWriteBody {
  name: string;
  value: string;
  masked: boolean;
  pipeline?: string;
  shared?: boolean;
}

export const scopeHelp =
  "Scope a secret to a pipeline to limit which runs can read it. Team-wide rows answer every pipeline in the team. There is no per-repository scope because a run's repository name is supplied by whoever starts it.";

// An unscoped row that is not shared answers no run; the CLI can still write
// one, so the page names it rather than calling it team-wide.
export function scopeLabel(row: Pick<StoredSecret, "pipeline" | "shared">) {
  if (row.pipeline) return `Pipeline: ${row.pipeline}`;
  return row.shared ? "Team" : "No runs (operator reads only)";
}

export function rowKey(row: Pick<StoredSecret, "name" | "pipeline">): string {
  return `${row.name}\u0000${row.pipeline ?? ""}`;
}

export function splitSecrets(rows: StoredSecret[]): {
  secrets: StoredSecret[];
  variables: StoredSecret[];
} {
  const sorted = [...rows].sort(
    (a, b) =>
      a.name.localeCompare(b.name) ||
      (a.pipeline ?? "").localeCompare(b.pipeline ?? ""),
  );
  return {
    secrets: sorted.filter((r) => r.masked),
    variables: sorted.filter((r) => !r.masked),
  };
}

const nameCharset = /^[A-Za-z0-9._/-]+$/;

// Mirrors internal/secretname.Validate so the form refuses what the
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
  if (draft.scope === "pipeline" && draft.pipeline.trim() === "") {
    return "Enter the pipeline this row is scoped to.";
  }
  if (draft.masked && draft.value === "") return "Enter the secret's value.";
  return null;
}

export function draftBody(draft: SecretDraft): SecretWriteBody {
  if (draft.scope === "pipeline") {
    return {
      name: draft.name,
      value: draft.value,
      masked: draft.masked,
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

// An update rewrites one existing row, so it keeps that row's scope, sharing
// and kind and changes only the value.
export function updateBody(row: StoredSecret, value: string): SecretWriteBody {
  const body: SecretWriteBody = { name: row.name, value, masked: row.masked };
  if (row.pipeline) body.pipeline = row.pipeline;
  if (row.shared) body.shared = true;
  return body;
}

export function existingRow(
  rows: StoredSecret[],
  draft: SecretDraft,
): StoredSecret | undefined {
  const pipeline = draft.scope === "pipeline" ? draft.pipeline.trim() : "";
  return rows.find(
    (r) => r.name === draft.name && (r.pipeline ?? "") === pipeline,
  );
}

export async function listSecrets(): Promise<StoredSecret[]> {
  const res = await send("GET", "/api/v1/secrets", "List secrets");
  return asList<StoredSecret>(await res.json(), "secrets");
}

export async function writeSecret(body: SecretWriteBody): Promise<void> {
  await send("POST", "/api/v1/secrets", `Save ${body.name}`, body);
}

export async function deleteSecret(
  row: Pick<StoredSecret, "name" | "pipeline">,
): Promise<void> {
  const query = row.pipeline
    ? `?pipeline=${encodeURIComponent(row.pipeline)}`
    : "";
  await send(
    "DELETE",
    `/api/v1/secrets/${encodeURIComponent(row.name)}${query}`,
    `Delete ${row.name}`,
  );
}
