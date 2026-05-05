"use client";

import { useEffect, useState } from "react";
import {
  getConnectionStatus,
  onConnectionStatusChange,
  type ConnectionStatus,
} from "@/lib/api";

export default function ConnectionBanner() {
  const [status, setStatus] = useState<ConnectionStatus>("ok");

  useEffect(() => {
    setStatus(getConnectionStatus());
    return onConnectionStatusChange(setStatus);
  }, []);

  if (status === "ok") return null;

  const messages: Record<Exclude<ConnectionStatus, "ok">, { text: string; hint: string }> = {
    unreachable: {
      text: "Cannot reach the sparkwing controller",
      hint: "The API may be down or your network connection was interrupted. Data shown may be stale.",
    },
    unauthorized: {
      text: "Authentication failed",
      hint: "The API token is missing or invalid. Check that SPARKWING_API_TOKEN is set in the web deployment.",
    },
  };

  const { text, hint } = messages[status];

  return (
    <div className="bg-red-900/80 border-b border-red-700 px-4 py-2 text-sm text-red-100 flex items-center gap-2">
      <span className="inline-block w-2 h-2 rounded-full bg-red-400 animate-pulse" />
      <span className="font-medium">{text}</span>
      <span className="text-red-300">&mdash; {hint}</span>
    </div>
  );
}
