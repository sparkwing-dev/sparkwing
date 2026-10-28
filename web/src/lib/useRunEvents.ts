
import { useEffect, useRef } from "react";
import { getRunEventsStreamUrl, type RunEvent } from "./api";
import { openEventStream } from "./eventStream";

// RUN_EVENT_KINDS are the named events the controller's run stream sends; the
// store's TestConsoleListensForEveryRunEventKind fails when it emits one not listed.
export const RUN_EVENT_KINDS = [
  "run_started",
  "run_cancel_requested",
  "run_success",
  "run_failed",
  "run_cancelled",
  "job_attempt_imported",
  "job_ready",
  "job_requeued",
  "job_revoked",
  "job_claimed",
  "job_approval_pending",
  "job_approval_ready",
  "job_expansion_ready",
  "job_started",
  "job_retrying",
  "job_cancel_requested",
  "job_succeeded",
  "job_failed",
  "job_cancelled",
  "job_skipped",
  "job_superseded",
  "job_memoized",
] as const;

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
    const es = openEventStream(url);

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

    for (const k of RUN_EVENT_KINDS) {
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
