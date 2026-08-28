
import { useEffect, useRef } from "react";
import { getRunEventsStreamUrl, type RunEvent } from "./api";

export type RunEventKind =
  | "node_started"
  | "node_succeeded"
  | "node_failed"
  | "node_cancelled"
  | "node_skipped"
  | "node_paused"
  | "node_resumed"
  | "cache_hit"
  | "attempt_retry"
  | "approval_requested"
  | "approval_resolved"
  | "expansion_generated"
  | "stream_end";

export interface UseRunEventsOptions {
  onEvent?: (event: RunEvent) => void;
  onEnd?: () => void;
  onError?: () => void;
}

export function useRunEvents(
  runID: string | null,
  opts: UseRunEventsOptions,
): void {
  const onEventRef = useRef(opts.onEvent);
  const onEndRef = useRef(opts.onEnd);
  const onErrorRef = useRef(opts.onError);
  useEffect(() => {
    onEventRef.current = opts.onEvent;
    onEndRef.current = opts.onEnd;
    onErrorRef.current = opts.onError;
  }, [opts.onEvent, opts.onEnd, opts.onError]);

  useEffect(() => {
    if (!runID) return;
    const url = getRunEventsStreamUrl(runID);
    const es = new EventSource(url, { withCredentials: true });

    const handle = (e: MessageEvent) => {
      if (!e.data) return;
      let parsed: RunEvent;
      try {
        parsed = JSON.parse(e.data as string) as RunEvent;
      } catch {
        return;
      }
      onEventRef.current?.(parsed);
    };

    const kinds: RunEventKind[] = [
      "node_started",
      "node_succeeded",
      "node_failed",
      "node_cancelled",
      "node_skipped",
      "node_paused",
      "node_resumed",
      "cache_hit",
      "attempt_retry",
      "approval_requested",
      "approval_resolved",
      "expansion_generated",
    ];
    for (const k of kinds) {
      es.addEventListener(k, handle as EventListener);
    }
    es.addEventListener("stream_end", () => {
      onEndRef.current?.();
      es.close();
    });
    es.onerror = () => {
      onErrorRef.current?.();
    };

    return () => {
      es.close();
    };
  }, [runID]);
}
