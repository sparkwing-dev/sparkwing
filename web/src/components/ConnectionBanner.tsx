"use client";

import { useEffect, useState } from "react";
import {
  getConnectionStatus,
  onConnectionStatusChange,
  type ConnectionStatus,
} from "@/lib/api";
import { connectionMessage } from "@/lib/connectionMessage";
import { localDashboard } from "@/lib/localSession";

export default function ConnectionBanner() {
  const [status, setStatus] = useState<ConnectionStatus>(getConnectionStatus);

  useEffect(() => onConnectionStatusChange(setStatus), []);

  if (status === "ok") return null;

  const { text, hint, signIn } = connectionMessage(status, localDashboard());

  return (
    <div className="bg-red-900/80 border-b border-red-700 px-4 py-2 text-sm text-red-100 flex items-center gap-2">
      <span className="inline-block w-2 h-2 rounded-full bg-red-400 animate-pulse" />
      <span className="font-medium">{text}</span>
      <span className="text-red-300">&mdash; {hint}</span>
      {signIn ? (
        <a className="underline font-medium" href="/login">
          Sign in again
        </a>
      ) : null}
    </div>
  );
}
