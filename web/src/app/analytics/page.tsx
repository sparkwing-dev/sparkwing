"use client";

import TrendCharts from "@/components/TrendCharts";

export default function AnalyticsPage() {
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

      <Section title="Trends">
        <TrendCharts />
      </Section>

      <Section title="Slowest pipelines">
        <Placeholder>
          Coming soon: p50 / p95 duration over the last 7 days, grouped by
          pipeline.
        </Placeholder>
      </Section>

      <Section title="Failure clustering">
        <Placeholder>
          Coming soon: failure rate by branch and by pipeline; recent flake
          detection (passed last N then failing).
        </Placeholder>
      </Section>

      <Section title="Agent utilization">
        <Placeholder>
          Coming soon: busy / idle time per runner type over the last 24h.
        </Placeholder>
      </Section>
    </div>
  );
}

function Section({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div className="mb-6">
      <h2 className="text-xs font-bold uppercase tracking-wider text-[var(--muted)] mb-2">
        {title}
      </h2>
      {children}
    </div>
  );
}

function Placeholder({ children }: { children: React.ReactNode }) {
  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-6 text-sm text-[var(--muted)]">
      {children}
    </div>
  );
}
