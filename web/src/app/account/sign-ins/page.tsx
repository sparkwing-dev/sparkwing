"use client";

import Link from "next/link";
import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import { Notice, Panel, errorText } from "@/components/TeamShell";
import {
  HowAccountsWork,
  LinkNoticeBox,
  SignInRow,
} from "@/components/SignInsForms";
import { toast } from "@/components/Toasts";
import { readCSRFCookie } from "@/lib/csrfCookie";
import {
  type LinkNotice,
  type SignIns,
  getSignIns,
  linkNotice,
  providerLabel,
  reauthMessage,
  signInRows,
  unlinkSignIn,
} from "@/lib/signIns";
import type { Me } from "@/lib/teams";
import { useTeamState } from "@/lib/useTeam";

export default function SignInsPage() {
  const state = useTeamState();
  if (state.status === "loading") return <Notice>Loading…</Notice>;
  if (state.status !== "ready") {
    return (
      <Notice>
        Accounts exist only on a controller with teams enabled, for people who
        sign in with Google or GitHub.
      </Notice>
    );
  }
  return <LinkedSignIns me={state.me} />;
}

function noSubscribe() {
  return () => {};
}

function emptyToken() {
  return "";
}

function LinkedSignIns({ me }: { me: Me }) {
  const csrfToken = useSyncExternalStore(
    noSubscribe,
    readCSRFCookie,
    emptyToken,
  );
  const [signIns, setSignIns] = useState<SignIns | null>(null);
  const [loadError, setLoadError] = useState("");
  const [notice, setNotice] = useState<LinkNotice | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      setSignIns(await getSignIns());
      setLoadError("");
    } catch (err) {
      setLoadError(errorText(err));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  // The link's result arrives once in the URL; it is shown, then dropped so
  // a reload does not show it again.
  useEffect(() => {
    const result = linkNotice(window.location.search);
    if (!result) return;
    setNotice(result);
    window.history.replaceState(null, "", window.location.pathname);
  }, []);

  async function unlink(provider: string) {
    const label = providerLabel(provider);
    const appNote =
      provider === "github"
        ? " GitHub App installations you connected stay connected to their teams."
        : "";
    if (
      !window.confirm(
        `Unlink ${label}? You won't be able to sign in with it.${appNote}`,
      )
    ) {
      return;
    }
    setBusy(true);
    try {
      const res = await unlinkSignIn(provider);
      if (res.kind === "reauth") {
        setNotice({ tone: "error", message: reauthMessage, explain: false });
      } else if (res.kind === "last") {
        setNotice({
          tone: "error",
          message: `${label} is your only way to sign in. Link another before unlinking it.`,
          explain: false,
        });
      } else {
        setNotice(null);
        toast(`Unlinked ${label}`, "success");
      }
      await load();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setBusy(false);
    }
  }

  const rows = signIns ? signInRows(signIns) : [];
  const linkedCount = signIns?.identities.length ?? 0;
  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-2xl mx-auto w-full space-y-4">
      <div>
        <div className="text-xs text-[var(--muted)] mb-1">
          <Link href="/account" className="underline">
            Account
          </Link>
        </div>
        <h1 className="text-xl font-bold mb-1">Linked sign-ins</h1>
        <div className="text-sm text-[var(--muted)]">
          Ways to sign in to {me.user.email}. Linking a sign-in never changes
          this email.
        </div>
      </div>
      {notice ? <LinkNoticeBox notice={notice} /> : null}
      <Panel title="Sign-in methods">
        {loadError ? (
          <div className="p-4 text-sm text-amber-300">{loadError}</div>
        ) : !signIns ? (
          <div className="p-4 text-sm text-[var(--muted)]">Loading…</div>
        ) : (
          <ul>
            {rows.map((row) => (
              <SignInRow
                key={row.provider}
                provider={row.provider}
                linked={row.linked}
                onlyMethod={row.linked !== null && linkedCount <= 1}
                csrfToken={csrfToken}
                busy={busy}
                onUnlink={unlink}
              />
            ))}
          </ul>
        )}
      </Panel>
      <p className="text-xs text-[var(--muted)]">
        Linking and unlinking need a sign-in from the last 10 minutes. Unlinking
        signs you out everywhere else.
      </p>
      <HowAccountsWork />
    </div>
  );
}
