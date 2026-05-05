"use client";

import { useEffect, useState } from "react";
import Tooltip from "./Tooltip";

function heartbeatHealth(lastHeartbeat: string | undefined): { color: string; label: string; age: number | null } {
  if (!lastHeartbeat) return { color: "bg-gray-500", label: "No heartbeat", age: null };
  const age = (Date.now() - new Date(lastHeartbeat).getTime()) / 1000;
  if (age < 10) return { color: "bg-green-400 animate-pulse", label: `Healthy (${Math.round(age)}s ago)`, age };
  if (age < 20) return { color: "bg-yellow-400", label: `Degraded (${Math.round(age)}s ago)`, age };
  return { color: "bg-red-400", label: `Lost (${Math.round(age)}s ago)`, age };
}

export default function HeartbeatDot({ lastHeartbeat }: { lastHeartbeat?: string }) {
  const [, setTick] = useState(0);
  useEffect(() => {
    const i = setInterval(() => setTick((t) => t + 1), 2000);
    return () => clearInterval(i);
  }, []);

  const { color, label } = heartbeatHealth(lastHeartbeat);

  return (
    <Tooltip content={label}>
      <span className={`inline-block w-2 h-2 rounded-full ${color}`} />
    </Tooltip>
  );
}

export function HeartbeatLabel({ lastHeartbeat }: { lastHeartbeat?: string }) {
  const [, setTick] = useState(0);
  useEffect(() => {
    const i = setInterval(() => setTick((t) => t + 1), 2000);
    return () => clearInterval(i);
  }, []);

  const { color, label } = heartbeatHealth(lastHeartbeat);

  return (
    <span className="inline-flex items-center gap-1.5">
      <span className={`inline-block w-2 h-2 rounded-full ${color}`} />
      <span className="text-[var(--foreground)]">{label}</span>
    </span>
  );
}
