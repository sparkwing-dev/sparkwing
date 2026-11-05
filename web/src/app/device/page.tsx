"use client";

import { type FormEvent, useState } from "react";
import {
  Notice,
  Panel,
  buttonClass,
  errorText,
  inputClass,
  quietButtonClass,
} from "@/components/TeamShell";
import {
  type DeviceRequest,
  type DeviceRunner,
  approveDeviceRequest,
  denyDeviceRequest,
  deviceCodeComplete,
  deviceCodeHasForeignLetter,
  expiryText,
  formatDeviceCode,
  grantText,
  lookupDeviceRequest,
  normalizeDeviceCode,
} from "@/lib/deviceLogin";
import { useCurrentTime } from "@/lib/useCurrentTime";
import { useTeamState } from "@/lib/useTeam";

export default function DevicePage() {
  const state = useTeamState();
  if (state.status === "loading") return <Notice>Loading…</Notice>;
  if (state.status !== "ready") {
    return (
      <Notice>
        Signing in a terminal from the browser needs a controller with team
        sign-in, and an account that belongs to a team. Sign in with Google or
        GitHub on that controller, then open this page again.
      </Notice>
    );
  }
  return <DeviceLogin />;
}

type Step =
  | { kind: "enter" }
  | { kind: "confirm"; request: DeviceRequest }
  | { kind: "done"; approved: boolean };

function DeviceLogin() {
  const [letters, setLetters] = useState("");
  const [step, setStep] = useState<Step>({ kind: "enter" });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  function startOver() {
    setLetters("");
    setError("");
    setStep({ kind: "enter" });
  }

  async function lookUp(event: FormEvent) {
    event.preventDefault();
    if (!deviceCodeComplete(letters)) return;
    setBusy(true);
    setError("");
    try {
      setStep({ kind: "confirm", request: await lookupDeviceRequest(letters) });
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  async function decide(approve: boolean) {
    setBusy(true);
    setError("");
    try {
      if (approve) await approveDeviceRequest(letters);
      else await denyDeviceRequest(letters);
      setStep({ kind: "done", approved: approve });
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-2xl mx-auto w-full space-y-4">
      <div>
        <h1 className="text-xl font-bold mb-1">Sign in a terminal</h1>
        <p className="text-sm text-[var(--muted)]">
          Enter the code your terminal shows after <code>sparkwing login</code>.
        </p>
      </div>
      {error ? (
        <div
          role="alert"
          className="rounded-[var(--radius-control)] border border-amber-500/40 bg-amber-500/10 text-amber-200 p-3 text-sm"
        >
          {error}
        </div>
      ) : null}
      {step.kind === "enter" ? (
        <EnterCode
          letters={letters}
          busy={busy}
          onChange={(value) => setLetters(normalizeDeviceCode(value))}
          onSubmit={lookUp}
        />
      ) : step.kind === "confirm" ? (
        <Confirm
          request={step.request}
          code={formatDeviceCode(letters)}
          busy={busy}
          onDecide={decide}
          onStartOver={startOver}
        />
      ) : (
        <Done approved={step.approved} onStartOver={startOver} />
      )}
    </div>
  );
}

function EnterCode({
  letters,
  busy,
  onChange,
  onSubmit,
}: {
  letters: string;
  busy: boolean;
  onChange: (value: string) => void;
  onSubmit: (event: FormEvent) => void;
}) {
  const foreign = deviceCodeHasForeignLetter(letters);
  return (
    <Panel title="Code">
      <form onSubmit={onSubmit} className="p-4 space-y-3">
        <label htmlFor="device-code" className="block text-sm">
          Device code
        </label>
        {/* The code is typed, never taken from the link, so a link someone
            else sent cannot approve a request on its own. */}
        <input
          id="device-code"
          name="device-code"
          className={`${inputClass} w-full font-mono tracking-widest text-lg`}
          value={formatDeviceCode(letters)}
          onChange={(event) => onChange(event.target.value)}
          placeholder="XXXX-XXXX"
          autoComplete="off"
          autoCapitalize="characters"
          spellCheck={false}
          aria-describedby="device-code-hint"
          disabled={busy}
        />
        <p id="device-code-hint" className="text-xs text-[var(--muted)]">
          {foreign
            ? "Codes use only consonants, without A, E, I, O, U or Y. Check the code in your terminal."
            : "Eight letters. The hyphen is optional."}
        </p>
        <button
          type="submit"
          className={buttonClass}
          disabled={busy || !deviceCodeComplete(letters)}
        >
          Continue
        </button>
      </form>
    </Panel>
  );
}

function Confirm({
  request,
  code,
  busy,
  onDecide,
  onStartOver,
}: {
  request: DeviceRequest;
  code: string;
  busy: boolean;
  onDecide: (approve: boolean) => void;
  onStartOver: () => void;
}) {
  const now = useCurrentTime(true, 15_000);
  const runner = request.kind === "runner" ? request.runner : undefined;
  const heading = runner
    ? `Approve runner ${runner.name}?`
    : `Approve sign-in for ${request.machine}?`;
  return (
    <section aria-labelledby="device-confirm" className="space-y-4">
      <h2 id="device-confirm" className="text-lg font-bold">
        {heading}
      </h2>
      <div className="rounded-[var(--radius-control)] border border-amber-500/40 bg-amber-500/10 text-amber-200 p-3 text-sm">
        Only approve a request you started yourself in a terminal in the last
        few minutes. If someone sent you this code, deny it.
      </div>
      <Panel title="Request">
        <dl className="p-4 text-sm grid grid-cols-[max-content_1fr] gap-x-4 gap-y-2">
          <dt className="text-[var(--muted)]">Code</dt>
          <dd className="font-mono tracking-widest">{code}</dd>
          <dt className="text-[var(--muted)]">Team</dt>
          <dd>{request.team}</dd>
          {request.machine ? (
            <>
              <dt className="text-[var(--muted)]">Machine</dt>
              <dd>{request.machine}</dd>
            </>
          ) : null}
          <dt className="text-[var(--muted)]">Expires</dt>
          <dd>{expiryText(request.expires_at, now)}</dd>
        </dl>
      </Panel>
      {runner ? <RunnerDetails runner={runner} team={request.team} /> : null}
      <Panel title="What it can do">
        <div className="p-4 text-sm space-y-3">
          <p>
            {runner
              ? `A runner token for ${request.team} with:`
              : `A CLI token for ${request.team} with:`}
          </p>
          <ul className="list-disc pl-5 space-y-1">
            {request.grants.map((grant) => (
              <li key={grant}>
                {grantText(grant)}{" "}
                <span className="text-xs text-[var(--muted)] font-mono">
                  {grant}
                </span>
              </li>
            ))}
          </ul>
          {runner ? null : (
            <p className="text-[var(--muted)]">
              The token expires in 90 days unless revoked. It belongs to this
              browser sign-in, so signing out here revokes it too.
            </p>
          )}
        </div>
      </Panel>
      <div className="flex items-center gap-3">
        <button
          type="button"
          className={buttonClass}
          disabled={busy}
          onClick={() => onDecide(true)}
        >
          Approve
        </button>
        <button
          type="button"
          className={quietButtonClass}
          disabled={busy}
          onClick={() => onDecide(false)}
        >
          Deny
        </button>
        <button
          type="button"
          className="text-sm underline text-[var(--muted)] disabled:opacity-50"
          disabled={busy}
          onClick={onStartOver}
        >
          Start over
        </button>
      </div>
    </section>
  );
}

function RunnerDetails({
  runner,
  team,
}: {
  runner: DeviceRunner;
  team: string;
}) {
  if (runner.personal) {
    return (
      <Panel title="Runner">
        <p className="p-4 text-sm">
          A personal runner on your own account in {team}.
        </p>
      </Panel>
    );
  }
  return (
    <Panel title="Runner">
      <dl className="p-4 text-sm grid grid-cols-[max-content_1fr] gap-x-4 gap-y-2">
        <dt className="text-[var(--muted)]">Kind</dt>
        <dd>Shared with {team}</dd>
        <dt className="text-[var(--muted)]">Repositories</dt>
        <dd>
          {runner.repos.length > 0 ? (
            <ul>
              {runner.repos.map((repo) => (
                <li key={repo} className="font-mono">
                  {repo}
                </li>
              ))}
            </ul>
          ) : (
            "None listed"
          )}
        </dd>
        <dt className="text-[var(--muted)]">Trust</dt>
        <dd>{runner.trust || "Default"}</dd>
        <dt className="text-[var(--muted)]">Reserved pools</dt>
        <dd>
          {runner.reserved_pools.length > 0
            ? runner.reserved_pools.join(", ")
            : "None"}
        </dd>
        <dt className="text-[var(--muted)]">Platform</dt>
        <dd>{runner.platform || "Not set"}</dd>
        <dt className="text-[var(--muted)]">Launcher</dt>
        <dd>{runner.launcher ? "Yes" : "No"}</dd>
      </dl>
    </Panel>
  );
}

function Done({
  approved,
  onStartOver,
}: {
  approved: boolean;
  onStartOver: () => void;
}) {
  return (
    <div className="space-y-3">
      <div
        role="status"
        className="rounded-[var(--radius-control)] border border-emerald-500/40 bg-emerald-500/10 text-emerald-200 p-3 text-sm"
      >
        {approved
          ? "Approved. Return to your terminal; it finishes signing in on its own."
          : "Denied. The terminal reports the request as denied."}
      </div>
      <button type="button" className={quietButtonClass} onClick={onStartOver}>
        Enter another code
      </button>
    </div>
  );
}
