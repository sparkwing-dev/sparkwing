"use client";

import {
  buttonClass,
  dangerButtonClass,
  quietButtonClass,
} from "@/components/TeamShell";
import {
  type LinkNotice,
  type LinkedSignIn,
  howAccountsWorkId,
  linkFormAction,
  providerLabel,
} from "@/lib/signIns";

// The two ways to deal with a second account, shown in the explanation and
// again beside a refusal to link another account's sign-in.
export function SecondAccountOptions() {
  return (
    <>
      <ul className="list-disc pl-5 space-y-1">
        <li>
          <strong>Keep both.</strong> From the other account, invite this one to
          its team, as an owner if you like. Then switch between the teams with
          the team switcher.
        </li>
        <li>
          <strong>Delete the other one.</strong> Sign in to it, delete it on its
          Account page, then link its sign-in here. Deleting it also deletes the
          teams where it is the only member, with everything in them.
        </li>
      </ul>
      <p>We don&apos;t currently offer combining accounts.</p>
    </>
  );
}

export function HowAccountsWork() {
  return (
    <section
      id={howAccountsWorkId}
      className="rounded-[var(--radius-control)] border border-[var(--border)] p-4 text-sm space-y-3"
    >
      <h2 className="text-base font-semibold">How accounts work</h2>
      <p>
        Your account holds your sign-in methods and your team memberships. Runs,
        secrets, credits and machines belong to teams, not to accounts.
      </p>
      <p>
        Linking adds another way to sign in to this account. It doesn&apos;t
        bring anything over from another Sparkwing account.
      </p>
      <h3 className="font-semibold">If you have a second Sparkwing account</h3>
      <SecondAccountOptions />
    </section>
  );
}

export function LinkNoticeBox({ notice }: { notice: LinkNotice }) {
  const tone =
    notice.tone === "success"
      ? "border-emerald-500/40 bg-emerald-500/10 text-emerald-200"
      : "border-amber-500/40 bg-amber-500/10 text-amber-200";
  return (
    <div
      role={notice.tone === "error" ? "alert" : "status"}
      className={`rounded-[var(--radius-control)] border p-3 text-sm space-y-2 ${tone}`}
    >
      <p>{notice.message}</p>
      {notice.explain ? (
        <>
          <SecondAccountOptions />
          <p>
            <a href={`#${howAccountsWorkId}`} className="underline">
              How accounts work
            </a>
          </p>
        </>
      ) : null}
    </div>
  );
}

export function SignInRow({
  provider,
  linked,
  onlyMethod,
  csrfToken,
  busy,
  onUnlink,
}: {
  provider: string;
  linked: LinkedSignIn | null;
  onlyMethod: boolean;
  csrfToken: string;
  busy: boolean;
  onUnlink: (provider: string) => void;
}) {
  const label = providerLabel(provider);
  return (
    <li className="px-4 py-3 border-b border-[var(--border)] last:border-0 flex items-center gap-3">
      <div className="flex-1 min-w-0">
        <div className="text-sm font-medium">{label}</div>
        <div className="text-xs text-[var(--muted)] truncate">
          {linked
            ? `Linked${linked.email ? ` · ${linked.email}` : ""}`
            : "Not linked"}
        </div>
      </div>
      {linked ? (
        <button
          type="button"
          className={onlyMethod ? quietButtonClass : dangerButtonClass}
          disabled={busy || onlyMethod}
          title={
            onlyMethod
              ? "This is your only way to sign in. Link another first."
              : undefined
          }
          onClick={() => onUnlink(provider)}
        >
          Unlink
        </button>
      ) : (
        <form method="POST" action={linkFormAction(provider)}>
          <input type="hidden" name="csrf_token" value={csrfToken} />
          <button
            type="submit"
            className={buttonClass}
            disabled={busy || !csrfToken}
          >
            Link {label}
          </button>
        </form>
      )}
    </li>
  );
}
