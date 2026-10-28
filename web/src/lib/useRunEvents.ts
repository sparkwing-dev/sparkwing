
import { useEffect, useRef } from "react";
import { getRunEventsStreamUrl, type RunEvent } from "./api";
import { type EventStream, openEventStream } from "./eventStream";
import runEventKinds from "./runEventKinds.json";

// RUN_EVENT_KINDS is every kind the controller's run stream sends
// (runstate.RunEventKinds), written to runEventKinds.json by internal/controller's
// TestConsoleFixturesMatchTheWireTypes, so the stream is heard for each of them.
export const RUN_EVENT_KINDS: readonly string[] = runEventKinds;

// listenForRunEvents hears every run event kind on a stream.
export function listenForRunEvents(
  es: Pick<EventStream, "addEventListener">,
  handle: (e: MessageEvent) => void,
): void {
  for (const kind of RUN_EVENT_KINDS) es.addEventListener(kind, handle);
}

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

    listenForRunEvents(es, handle);
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
