"use client";

import React, { useEffect, useState, useCallback } from "react";
import { type ServiceStatus, getServiceHealth } from "@/lib/api";

function statusColor(status: string): string {
  if (status === "ok") return "bg-green-400";
  if (status === "degraded") return "bg-amber-400";
  if (status === "down") return "bg-red-400";
  return "bg-gray-400";
}

function statusText(status: string): string {
  if (status === "ok") return "Healthy";
  if (status === "degraded") return "Degraded";
  if (status === "down") return "Down";
  return "Unknown";
}

function LatencyBar({ ms, max }: { ms: number; max: number }) {
  const w = max > 0 ? Math.min(100, Math.round((ms / max) * 100)) : 0;
  const color =
    ms < 50
      ? "bg-green-400/60"
      : ms < 200
        ? "bg-amber-400/60"
        : "bg-red-400/60";
  return (
    <div className="flex items-center gap-2">
      <div className="flex-1 bg-[var(--background)] rounded-full h-1.5 overflow-hidden">
        <div
          className={`h-full rounded-full ${color}`}
          style={{ width: `${w}%` }}
        />
      </div>
      <span className="text-[10px] font-mono text-[var(--muted)] w-10 text-right shrink-0">
        {ms}ms
      </span>
    </div>
  );
}

export default function ServiceHealth() {
  const [services, setServices] = useState<ServiceStatus[]>([]);

  const refresh = useCallback(async () => {
    const data = await getServiceHealth();
    setServices(data);
  }, []);

  useEffect(() => {
    refresh();
    const i = setInterval(refresh, 15_000);
    return () => clearInterval(i);
  }, [refresh]);

  if (services.length === 0) {
    return (
      <div className="text-xs text-[var(--muted)] p-4">
        Loading service health...
      </div>
    );
  }

  const maxLatency = Math.max(1, ...services.map((s) => s.latency_ms));
  const allOk = services.every((s) => s.status === "ok");

  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-4">
      <div className="flex items-center justify-between mb-3">
        <div className="text-xs font-medium text-[var(--muted)]">
          Service Health
        </div>
        <div className="flex items-center gap-1.5">
          <div
            className={`w-2 h-2 rounded-full ${allOk ? "bg-green-400" : "bg-amber-400 animate-pulse"}`}
          />
          <span className="text-[10px] text-[var(--muted)]">
            {allOk ? "All systems operational" : "Issues detected"}
          </span>
        </div>
      </div>
      <div className="space-y-2">
        {services.map((svc) => (
          <React.Fragment key={svc.name}>
            <div className="flex items-center gap-3">
              <div
                className={`w-2 h-2 rounded-full shrink-0 ${statusColor(svc.status)}`}
              />
              <span className="text-xs text-[var(--foreground)] w-24 shrink-0 font-mono">
                {svc.name}
              </span>
              <span
                className={`text-[10px] w-16 shrink-0 ${svc.status === "ok" ? "text-green-400" : svc.status === "down" ? "text-red-400" : "text-amber-400"}`}
              >
                {statusText(svc.status)}
              </span>
              <div className="flex-1">
                <LatencyBar ms={svc.latency_ms} max={maxLatency} />
              </div>
              {svc.error && !svc.problems?.length && (
                <span
                  className="text-[10px] text-red-400 truncate max-w-32"
                  title={svc.error}
                >
                  {svc.error}
                </span>
              )}
            </div>
            {svc.problems && svc.problems.length > 0 && (
              <div className="ml-[calc(0.5rem+8px+0.75rem)] mt-0.5 space-y-0.5">
                {svc.problems.map((msg, i) => (
                  <div
                    key={i}
                    className="text-[10px] text-amber-400/80 leading-tight"
                  >
                    {msg}
                  </div>
                ))}
              </div>
            )}
          </React.Fragment>
        ))}
      </div>
    </div>
  );
}
