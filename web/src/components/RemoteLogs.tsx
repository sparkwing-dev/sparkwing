"use client";

import { useEffect, useState } from "react";

import { LogBucketViewFromRaw } from "@/components/LogBucketView";

interface Props {
  jobId: string;
  logsUrl: string;
}

/**
 * RemoteLogs fetches a completed job's bytes from sparkwing-logs
 * (via the Job's `logs_url`) and renders them through the existing
 * LogBucketViewFromRaw viewer. Used as a fallback when the Job
 * response no longer carries `result.logs` inline — which is the
 * case for cached jobs after the controller stopped duplicating
 * logs into the cache table.
 *
 * Bounded fetch: cap at 2MB to keep the browser responsive. Users
 * who need the full stream can drop to the CLI.
 */
export default function RemoteLogs({ jobId, logsUrl }: Props) {
  const [text, setText] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const url = `${logsUrl}?offset=0&limit=${2 * 1024 * 1024}`;
        const token =
          (typeof window !== "undefined" &&
            ((window as unknown as Record<string, unknown>)
              .__SPARKWING_TOKEN__ as string)) ||
          "";
        const res = await fetch(url, {
          headers: token ? { Authorization: `Bearer ${token}` } : {},
        });
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
