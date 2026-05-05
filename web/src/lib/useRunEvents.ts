// Subscribe to a run's structured SSE event stream.
//
// The server (`/api/v1/runs/{id}/events/stream`) emits `node_started`,
// `node_succeeded`, `approval_resolved`, and friends as named SSE
// events with an `id:` line carrying the store seq. The browser
// EventSource echoes the last seen id back as Last-Event-ID on
// reconnect, so resumption is transparent — the handler just needs a
// cheap callback to re-sync client state (typically a refetch of
// getRun).
//
// Design choice: the hook does NOT try to mutate state per event.
// Events are lightweight "something changed" nudges; the consumer
// calls getRun to re-materialize a consistent snapshot. This avoids
// reimplementing the server's state-transition logic on both sides
// and keeps the hook small.

import { useEffect, useRef } from "react";
import { getRunEventsStreamUrl, type RunEvent } from "./api";

// The kind strings emitted by the Go handler. Kept as a union type
// so handlers can pattern-match; unknown kinds still fire the generic
// onEvent so new server-side kinds don't silently drop.
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
  // Fired on every event. Use for a coalesced refetch.
  onEvent?: (event: RunEvent) => void;
  // Fired when the server signals the run has terminated and the
  // event tail has drained. The connection is closed server-side;
  // clients should stop any fallback polling.
  onEnd?: () => void;
  // Fired on network errors / disconnect. The browser retries
  // automatically; this is just a signal for the consumer to engage
  // its fallback polling while the stream is down.
  onError?: () => void;
}

export function useRunEvents(
  runID: string | null,
  opts: UseRunEventsOptions,
): void {
  // Refs so we don't re-open the stream every render just because the
  // callback identities changed.
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
    // withCredentials forwards the session cookie so the SSE request
    // passes the dashboard's auth middleware — same path the per-node
    // log stream uses.
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

    // One listener per known kind so consumers can addEventListener
    // themselves for a subset if they want. A generic "message"
    // fallback catches any kind the server added after this file was
    // written.
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
