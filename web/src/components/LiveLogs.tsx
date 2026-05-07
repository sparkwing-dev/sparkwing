"use client";

import { useEffect, useState, useRef, useMemo } from "react";
import { parseLogLines } from "@/lib/logParser";
import LogBucketView from "@/components/LogBucketView";
import { getControllerUrl } from "@/lib/api";

// Token used to authenticate SSE subscribes against the logs service.
// EventSource can't set headers, so both sparkwing-logs and the
// controller's legacy /logs/ handler accept `?token=<token>` as a
// fallback.
function getToken(): string {
  if (typeof window === "undefined") return "";
  const win = window as unknown as Record<string, unknown>;
  if (typeof win.__SPARKWING_TOKEN__ === "string")
    return win.__SPARKWING_TOKEN__ as string;
  return process.env.NEXT_PUBLIC_API_TOKEN || "";
}

// buildLogSSEUrl picks the right endpoint + appends the token query
// param. Prefers the server-provided logs_url (sparkwing-logs public
// endpoint), falls back to the controller's legacy /logs/ path.
function buildLogSSEUrl(jobId: string, logsUrl?: string): string {
  const base = logsUrl || `${getControllerUrl()}/logs/${jobId}`;
  const token = getToken();
  if (!token) return base;
  const sep = base.includes("?") ? "&" : "?";
  return `${base}${sep}token=${encodeURIComponent(token)}`;
}

export default function LiveLogs({
  jobId,
  logsUrl,
  fallbackLogs,
}: {
  jobId: string;
  logsUrl?: string;
  fallbackLogs?: string;
}) {
  const [lines, setLines] = useState<string[]>([]);
  const [connected, setConnected] = useState(false);
  const [logStatus, setLogStatus] = useState<
    "connecting" | "connected" | "disconnected"
  >("connecting");
  const [source, setSource] = useState<"sparkwing-logs" | "controller">(
    logsUrl ? "sparkwing-logs" : "controller",
  );
  const bottomRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let es: EventSource | null = null;
    let retryCount = 0;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    let cancelled = false;

    function connect() {
      if (cancelled) return;
      const url = buildLogSSEUrl(jobId, logsUrl);
      es = new EventSource(url);
      setSource(logsUrl ? "sparkwing-logs" : "controller");

      es.onopen = () => {
        setConnected(true);
        setLogStatus("connected");
        retryCount = 0; // reset backoff on successful connection
      };

      es.onmessage = (event) => {
        const chunk = event.data as string;
        if (chunk.includes("\n")) {
          const parts = chunk.split("\n").filter((p) => p.length > 0);
          setLines((prev) => [...prev, ...parts]);
        } else {
          setLines((prev) => [...prev, chunk]);
        }
      };

      es.onerror = () => {
        setConnected(false);
        es?.close();
        // Auto-reconnect with exponential backoff (1s, 2s, 4s, 8s, max 30s)
        if (!cancelled && retryCount < 10) {
          const delay = Math.min(1000 * Math.pow(2, retryCount), 30_000);
          retryCount++;
          retryTimer = setTimeout(connect, delay);
        } else if (!cancelled) {
          setLogStatus("disconnected");
        }
      };
    }

    connect();

    return () => {
      cancelled = true;
      es?.close();
      if (retryTimer) clearTimeout(retryTimer);
    };
  }, [jobId, logsUrl]);

  // Auto-scroll to bottom on new lines
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [lines]);

  const parsed = useMemo(() => parseLogLines(lines), [lines.length]);
  const hasSteps = parsed.sections.some((s) => s.type === "step");

  // If SSE has no data yet, show fallback logs or a waiting indicator
  if (lines.length === 0 && !connected) {
    // If we have stored logs from the job result, show them
    if (fallbackLogs && fallbackLogs.trim().length > 0) {
      const fallbackParsed = parseLogLines(fallbackLogs.split("\n"));
      return <LogBucketView parsed={fallbackParsed} jobId={jobId} />;
    }
    if (logStatus === "disconnected") {
      return (
        <div className="flex items-center gap-2 text-sm text-[var(--muted)] p-4">
          <div className="w-2 h-2 bg-amber-400 rounded-full" />
          Waiting for logs — the runner may still be starting up.
        </div>
      );
    }
    return (
      <div className="flex items-center gap-2 text-sm text-[var(--muted)] p-4">
        <div className="w-2 h-2 bg-indigo-400 rounded-full animate-pulse" />
        Connecting to log stream...
      </div>
    );
  }

  return (
    <div>
      {connected && (
        <div className="flex items-center gap-2 text-xs text-green-400 mb-2">
          <div className="w-1.5 h-1.5 bg-green-400 rounded-full" />
          Live
          <span className="text-[var(--muted)]">· {source}</span>
        </div>
      )}
      {hasSteps ? (
        <>
          <LogBucketView parsed={parsed} />
          <div ref={bottomRef} />
        </>
      : (
        <pre className="text-xs font-mono leading-5 whitespace-pre-wrap text-[#c9d1d9]">
          {lines.map((line, i) => (
            <div key={i} className="flex hover:bg-white/5">
              <span className="select-none text-[#484f58] w-8 text-right mr-3 shrink-0">
                {i + 1}
              </span>
              <span
                className={
                  line.includes("PASS")
                    ? "text-green-400"
                    : line.includes("FAIL")
                      ? "text-red-400"
                      : line.includes("===")
                        ? "text-indigo-400 font-bold"
                        : line.startsWith(">")
                          ? "text-cyan-400"
                          : ""
                }
              >
                {line}
              </span>
            </div>
          ))}
          <div ref={bottomRef} />
        </pre>
      )}
    </div>
  );
}
