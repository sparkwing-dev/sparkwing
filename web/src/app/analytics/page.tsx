"use client";

import { Suspense } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import TrendCharts, {
  DEFAULT_TREND_RANGE,
  TREND_RANGES,
  trendRange,
} from "@/components/TrendCharts";

export default function AnalyticsPage() {
  return (
    <Suspense>
      <AnalyticsRoute />
    </Suspense>
  );
}

function AnalyticsRoute() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const range = trendRange(searchParams.get("range"));
  const setRange = (key: string) => {
    const next = new URLSearchParams(searchParams.toString());
    if (key === DEFAULT_TREND_RANGE) next.delete("range");
    else next.set("range", key);
    const qs = next.toString();
    router.replace(qs ? `/analytics?${qs}` : "/analytics", { scroll: false });
  };

  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-6xl mx-auto w-full">
      <div className="flex items-baseline justify-between mb-4">
        <h1 className="text-xl font-bold">Analytics</h1>
        <span className="text-[10px] font-mono text-[var(--muted)]">
          long-term run trends
        </span>
      </div>

      <p className="text-sm text-[var(--muted)] mb-6">
        Time-series view of run history. Per-pipeline detail and recency live on{" "}
        <a href="/runs" className="text-[var(--accent)] hover:underline">
          Runs
        </a>
        .
      </p>

      <Section
        title="Trends"
        action={
          <label className="flex items-center gap-2 text-xs text-[var(--muted)]">
            Range
            <select
              value={range.key}
              onChange={(event) => setRange(event.target.value)}
              className="px-2 py-1 rounded-sm border border-[var(--border)] bg-[var(--surface)] text-[var(--foreground)]"
            >
              {TREND_RANGES.map((r) => (
                <option key={r.key} value={r.key}>
                  Last {r.label}
                </option>
              ))}
            </select>
          </label>
        }
      >
        <TrendCharts hours={range.hours} />
      </Section>
    </div>
  );
}

function Section({
  title,
  action,
  children,
}: {
  title: string;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div className="mb-6">
      <div className="flex items-center justify-between gap-2 mb-2">
        <h2 className="text-xs font-bold uppercase tracking-wider text-[var(--muted)]">
          {title}
        </h2>
        {action}
      </div>
      {children}
    </div>
  );
}
