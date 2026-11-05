"use client";

import Link from "next/link";
import { useSyncExternalStore } from "react";
import CopyField from "./CopyField";

const subscribe = () => () => {};
const origin = () => window.location.origin;
const serverOrigin = () => "";

export default function CLISignIn({ teamSlug }: { teamSlug: string }) {
  const controller = useSyncExternalStore(subscribe, origin, serverOrigin);
  return <div className="space-y-3 py-4 text-sm">
    <p>Run this in your terminal, then open the link it prints and approve the code. This also replaces an expired CLI token.</p>
    {controller && <CopyField value={`sparkwing login --controller ${controller} --team ${teamSlug} --profile ${teamSlug}`} label="CLI sign-in command" multiline />}
    <p className="text-[var(--muted)]">Your team slug is <code>{teamSlug}</code>. Use it for <code>--team</code>, workflow variables and AWS trust policies.</p>
    <Link href="/device" className="inline-flex min-h-11 items-center text-indigo-300 underline">Enter a code from your terminal →</Link>
  </div>;
}
