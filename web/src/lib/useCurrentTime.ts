"use client";

import { useEffect, useState } from "react";

export function useCurrentTime(active = true, intervalMs = 1000): number {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (!active) return;
    const interval = window.setInterval(() => setNow(Date.now()), intervalMs);
    return () => window.clearInterval(interval);
  }, [active, intervalMs]);

  return now;
}
