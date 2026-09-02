"use client";

import { useEffect, useState } from "react";

import { LogBucketViewFromRaw } from "@/components/LogBucketView";

interface Props {
  jobId: string;
  logsUrl: string;
}

export default function RemoteLogs({ jobId, logsUrl }: Props) {
  const [text, setText] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const url = `${logsUrl}?offset=0&limit=${2 * 1024 * 1024}`;
        const res = await fetch(url);
        if (!res.ok) {
          throw new Error(`logs service returned ${res.status}`);
        }
        const body = await res.text();
        if (!cancelled) setText(body);
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [logsUrl]);

  if (error) {
    return (
      <div className="text-sm text-[var(--muted)]">
        Failed to load logs: {error}
      </div>
    );
  }
  if (text === null) {
    return <div className="text-sm text-[var(--muted)]">Loading logs…</div>;
  }
  if (text === "") {
    return (
      <div className="text-sm text-[var(--muted)]">
        No logs available for this job.
      </div>
    );
  }
  return <LogBucketViewFromRaw rawLog={text} jobId={jobId} />;
}
