"use client";

import { useCallback, useEffect, useState } from "react";
import TeamShell, {
  Panel,
  buttonClass,
  dangerButtonClass,
  errorText,
  inputClass,
  quietButtonClass,
} from "@/components/TeamShell";
import { toast } from "@/components/Toasts";
import { type Me, canManageTeam, unixSecondsISO } from "@/lib/teams";
import {
  type ScopeKind,
  type StoredSecret,
  deleteSecret,
  draftBody,
  draftProblem,
  existingRow,
  listSecrets,
  rowKey,
  scopeHelp,
  scopeLabel,
  splitSecrets,
  updateBody,
  writeSecret,
} from "@/lib/secrets";
import { fmtDateTime } from "@/lib/timeFormat";

export default function SecretsPage() {
  return <TeamShell>{(me) => <SecretsAndVariables me={me} />}</TeamShell>;
}

const labelClass =
  "block text-[10px] font-bold uppercase tracking-wider text-[var(--muted)] mb-1";

function updatedLine(row: StoredSecret): string {
  const when = row.updated_at
    ? fmtDateTime(unixSecondsISO(row.updated_at))
    : "";
  return [
    scopeLabel(row),
    when ? `updated ${when}` : "",
    row.principal ? `by ${row.principal}` : "",
  ]
    .filter(Boolean)
    .join(" · ");
}

function SecretsAndVariables({ me }: { me: Me }) {
  const owner = canManageTeam(me.active_team.role);
  const [rows, setRows] = useState<StoredSecret[] | null>(null);
  const [loadError, setLoadError] = useState("");
  const [busy, setBusy] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setRows(await listSecrets());
      setLoadError("");
    } catch (err) {
      setLoadError(errorText(err));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function remove(row: StoredSecret) {
    const kind = row.masked ? "secret" : "variable";
    if (
      !window.confirm(
        `Delete the ${kind} ${row.name} (${scopeLabel(row)})? Runs that read it fail from now on.`,
      )
    ) {
      return;
    }
    setBusy(rowKey(row));
    try {
      await deleteSecret(row);
      toast(`Deleted ${row.name}`, "success");
      await load();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setBusy(null);
    }
  }

  const { secrets, variables } = splitSecrets(rows ?? []);
  const listBody = (
    items: StoredSecret[],
    empty: string,
    render: (r: StoredSecret) => React.ReactNode,
  ) =>
    loadError ? (
      <div className="p-4 text-sm text-red-300">{loadError}</div>
    ) : rows === null ? (
      <div className="p-4 text-xs text-[var(--muted)]">Loading…</div>
    ) : items.length === 0 ? (
      <div className="p-4 text-xs text-[var(--muted)]">{empty}</div>
    ) : (
      <ul>{items.map(render)}</ul>
    );

  return (
    <>
      {owner ? (
        <CreateForm rows={rows ?? []} onSaved={load} />
      ) : (
        <div className="mb-6 text-sm text-[var(--muted)]">
          Owners add and change secrets and variables. You can see which exist
          and read the variables.
        </div>
      )}
      <Panel
        title="Secrets"
        hint="Write-only. A secret's value is never shown again after it is saved, to anyone; only a run of a pipeline it answers can read it."
      >
        {listBody(secrets, "No secrets yet.", (row) => (
          <SecretRow
            key={rowKey(row)}
            row={row}
            owner={owner}
            disabled={busy !== null}
            onDelete={() => remove(row)}
            onSaved={load}
          />
        ))}
      </Panel>
      <Panel
        title="Variables"
        hint="Plain configuration. Every member can read a variable's value, and runs see it unmasked in logs."
      >
        {listBody(variables, "No variables yet.", (row) => (
          <VariableRow
            key={rowKey(row)}
            row={row}
            owner={owner}
            disabled={busy !== null}
            onDelete={() => remove(row)}
            onSaved={load}
          />
        ))}
      </Panel>
    </>
  );
}

function SecretRow({
  row,
  owner,
  disabled,
  onDelete,
  onSaved,
}: {
  row: StoredSecret;
  owner: boolean;
  disabled: boolean;
  onDelete: () => void;
  onSaved: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState("");
  const [saving, setSaving] = useState(false);

  function close() {
    setValue("");
    setEditing(false);
  }

  async function save(e: React.FormEvent) {
    e.preventDefault();
    if (value === "") return;
    setSaving(true);
    try {
      await writeSecret(updateBody(row, value));
      toast(`Updated ${row.name}`, "success");
      close();
      await onSaved();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setSaving(false);
    }
  }

  return (
    <li className="px-4 py-3 border-b border-[var(--border)] last:border-0">
      <div className="flex items-center gap-3">
        <div className="flex-1 min-w-0">
          <div className="text-sm font-mono truncate">{row.name}</div>
          <div className="text-xs text-[var(--muted)] truncate">
            {updatedLine(row)}
          </div>
        </div>
        {owner && !editing ? (
          <>
            <button
              type="button"
              className={quietButtonClass}
              disabled={disabled}
              onClick={() => setEditing(true)}
            >
              Update
            </button>
            <button
              type="button"
              className={dangerButtonClass}
              disabled={disabled}
              onClick={onDelete}
            >
              Delete
            </button>
          </>
        ) : null}
      </div>
      {editing ? (
        <form onSubmit={save} className="mt-2 flex flex-col gap-2">
          <label htmlFor={`update-${rowKey(row)}`} className={labelClass}>
            New value for {row.name}
          </label>
          <textarea
            id={`update-${rowKey(row)}`}
            autoFocus
            rows={3}
            autoComplete="off"
            spellCheck={false}
            className={`${inputClass} w-full font-mono`}
            value={value}
            onChange={(e) => setValue(e.target.value)}
          />
          <div className="flex gap-2">
            <button
              type="submit"
              className={buttonClass}
              disabled={saving || value === ""}
            >
              {saving ? "Saving…" : "Replace value"}
            </button>
            <button type="button" className={quietButtonClass} onClick={close}>
              Cancel
            </button>
          </div>
        </form>
      ) : null}
    </li>
  );
}

function VariableRow({
  row,
  owner,
  disabled,
  onDelete,
  onSaved,
}: {
  row: StoredSecret;
  owner: boolean;
  disabled: boolean;
  onDelete: () => void;
  onSaved: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(row.value ?? "");
  const [saving, setSaving] = useState(false);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setSaving(true);
    try {
      await writeSecret(updateBody(row, value));
      toast(`Updated ${row.name}`, "success");
      setEditing(false);
      await onSaved();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setSaving(false);
    }
  }

  return (
    <li className="px-4 py-3 border-b border-[var(--border)] last:border-0">
      <div className="flex items-start gap-3">
        <div className="flex-1 min-w-0">
          <div className="text-sm font-mono truncate">{row.name}</div>
          {editing ? (
            <form onSubmit={save} className="mt-2 flex flex-col gap-2">
              <textarea
                aria-label={`Value of ${row.name}`}
                autoFocus
                rows={Math.min(8, Math.max(2, value.split("\n").length))}
                spellCheck={false}
                className={`${inputClass} w-full font-mono`}
                value={value}
                onChange={(e) => setValue(e.target.value)}
              />
              <div className="flex gap-2">
                <button type="submit" className={buttonClass} disabled={saving}>
                  {saving ? "Saving…" : "Save"}
                </button>
                <button
                  type="button"
                  className={quietButtonClass}
                  onClick={() => {
                    setValue(row.value ?? "");
                    setEditing(false);
                  }}
                >
                  Cancel
                </button>
              </div>
            </form>
          ) : (
            <pre className="text-xs font-mono whitespace-pre-wrap break-all mt-1">
              {row.value ?? ""}
            </pre>
          )}
          <div className="text-xs text-[var(--muted)] truncate mt-1">
            {updatedLine(row)}
          </div>
        </div>
        {owner && !editing ? (
          <>
            <button
              type="button"
              className={quietButtonClass}
              disabled={disabled}
              onClick={() => {
                setValue(row.value ?? "");
                setEditing(true);
              }}
            >
              Edit
            </button>
            <button
              type="button"
              className={dangerButtonClass}
              disabled={disabled}
              onClick={onDelete}
            >
              Delete
            </button>
          </>
        ) : null}
      </div>
    </li>
  );
}

function CreateForm({
  rows,
  onSaved,
}: {
  rows: StoredSecret[];
  onSaved: () => Promise<void>;
}) {
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [masked, setMasked] = useState(true);
  const [scope, setScope] = useState<ScopeKind>("team");
  const [pipeline, setPipeline] = useState("");
  const [touched, setTouched] = useState(false);
  const [saving, setSaving] = useState(false);

  const draft = { name, value, masked, scope, pipeline };
  const problem = draftProblem(draft);
  const replaces = name ? existingRow(rows, draft) : undefined;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setTouched(true);
    if (problem) return;
    setSaving(true);
    try {
      await writeSecret(draftBody(draft));
      toast(`Saved ${masked ? "secret" : "variable"} ${name}`, "success");
      setName("");
      setValue("");
      setTouched(false);
      await onSaved();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setSaving(false);
    }
  }

  return (
    <Panel title="Add a secret or variable" hint={scopeHelp}>
      <form onSubmit={submit} className="flex flex-col gap-3 p-4" noValidate>
        <fieldset className="flex flex-wrap gap-4 text-sm">
          <legend className={labelClass}>Kind</legend>
          <label className="flex items-center gap-1.5">
            <input
              type="radio"
              name="secret-kind"
              checked={masked}
              onChange={() => setMasked(true)}
            />
            Secret (write-only, masked in logs)
          </label>
          <label className="flex items-center gap-1.5">
            <input
              type="radio"
              name="secret-kind"
              checked={!masked}
              onChange={() => setMasked(false)}
            />
            Variable (readable by members)
          </label>
        </fieldset>
        <div>
          <label htmlFor="secret-name" className={labelClass}>
            Name
          </label>
          <input
            id="secret-name"
            autoComplete="off"
            spellCheck={false}
            maxLength={256}
            placeholder="DEPLOY_KEY"
            className={`${inputClass} w-full font-mono`}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </div>
        <div>
          <label htmlFor="secret-value" className={labelClass}>
            Value
          </label>
          <textarea
            id="secret-value"
            rows={3}
            autoComplete="off"
            spellCheck={false}
            className={`${inputClass} w-full font-mono`}
            value={value}
            onChange={(e) => setValue(e.target.value)}
          />
        </div>
        <fieldset className="flex flex-wrap items-center gap-4 text-sm">
          <legend className={labelClass}>Scope</legend>
          <label className="flex items-center gap-1.5">
            <input
              type="radio"
              name="secret-scope"
              checked={scope === "team"}
              onChange={() => setScope("team")}
            />
            Team (every pipeline)
          </label>
          <label className="flex items-center gap-1.5">
            <input
              type="radio"
              name="secret-scope"
              checked={scope === "pipeline"}
              onChange={() => setScope("pipeline")}
            />
            Pipeline
          </label>
          {scope === "pipeline" ? (
            <input
              aria-label="Pipeline name"
              autoComplete="off"
              spellCheck={false}
              placeholder="pipeline name, e.g. deploy-web"
              className={`${inputClass} flex-1 min-w-[14rem] font-mono`}
              value={pipeline}
              onChange={(e) => setPipeline(e.target.value)}
            />
          ) : null}
        </fieldset>
        {touched && problem ? (
          <div role="alert" className="text-xs text-red-300">
            {problem}
          </div>
        ) : null}
        {replaces ? (
          <div className="text-xs text-amber-200">
            Saving replaces the existing{" "}
            {replaces.masked ? "secret" : "variable"} {replaces.name} (
            {scopeLabel(replaces)}).
          </div>
        ) : null}
        <div>
          <button type="submit" className={buttonClass} disabled={saving}>
            {saving ? "Saving…" : masked ? "Save secret" : "Save variable"}
          </button>
        </div>
      </form>
    </Panel>
  );
}
