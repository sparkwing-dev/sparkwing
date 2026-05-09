"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { type Run, getRuns } from "@/lib/api";
import ServiceHealth from "@/components/ServiceHealth";
import TrendCharts from "@/components/TrendCharts";
import MetricsPanel from "@/components/MetricsPanel";

const POLL_MS = 1500;

export default function Dashboard() {
  const [runs, setRuns] = useState<Run[]>([]);
  const [showMetrics, setShowMetrics] = useState(false);
  const [showTrends, setShowTrends] = useState(false);
  const [showHealth, setShowHealth] = useState(false);

  const loadRuns = useCallback(async () => {
    const list = await getRuns({ limit: 50 });
    setRuns(list);
  }, []);

  useEffect(() => {
    loadRuns();
    const tick = () => {
      if (document.hidden) return;
      loadRuns();
    };
    const i = window.setInterval(tick, POLL_MS);
    return () => window.clearInterval(i);
  }, [loadRuns]);

  const stats = useMemo(() => {
    const total = runs.length;
    const running = runs.filter((r) => r.status === "running").length;
    const passed = runs.filter((r) => r.status === "success").length;
    const failed = runs.filter((r) => r.status === "failed").length;
    return { total, running, passed, failed };
  }, [runs]);

  return (
    <div className="flex-1 overflow-y-auto p-6 max-w-7xl mx-auto w-full">
      <div className="grid grid-cols-4 gap-4 mb-6">
        <StatCard value={stats.total} label="Total runs" />
        <StatCard
          value={stats.running}
          label="Running"
          color="text-indigo-400"
        />
        <StatCard value={stats.passed} label="Passed" color="text-green-400" />
        <StatCard value={stats.failed} label="Failed" color="text-red-400" />
      </div>

      <CollapsibleSection
        label="Metrics"
        expanded={showMetrics}
        onToggle={() => setShowMetrics(!showMetrics)}
      >
        <MetricsPanel jobs={[]} agents={[]} />
      </CollapsibleSection>

      <CollapsibleSection
        label="Trends"
        expanded={showTrends}
        onToggle={() => setShowTrends(!showTrends)}
      >
        <TrendCharts />
      </CollapsibleSection>

      <CollapsibleSection
        label="Service Health"
        expanded={showHealth}
        onToggle={() => setShowHealth(!showHealth)}
      >
        <ServiceHealth />
      </CollapsibleSection>
    </div>
  );
}

function StatCard({
  value,
  label,
  color,
}: {
  value: number;
  label: string;
  color?: string;
}) {
  return (
    <div className="bg-[var(--surface)] border border-[var(--border)] rounded-lg p-4">
      <div className={`text-3xl font-bold ${color || ""}`}>{value}</div>
      <div className="text-xs text-[var(--muted)]">{label}</div>
    </div>
  );
}

function CollapsibleSection({
  label,
  expanded,
  onToggle,
  children,
}: {
  label: string;
  expanded: boolean;
  onToggle: () => void;
  children: React.ReactNode;
}) {
  return (
    <div className="mb-6">
      <button
        onClick={onToggle}
        className="text-xs text-[var(--muted)] hover:text-[var(--foreground)] transition-colors flex items-center gap-1 mb-2"
      >
        <span>{expanded ? "▾" : "▸"}</span>
        <span>{label}</span>
      </button>
      {expanded && children}
    </div>
  );
}
