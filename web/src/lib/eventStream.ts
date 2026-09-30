import { localAuthHeaders, localSession, signedIn } from "./localSession";

type Listener = (e: MessageEvent) => void;

// EventStream is the part of EventSource the dashboard uses. A browser
// EventSource cannot send an Authorization header, so a page holding a local
// session reads the stream through fetch instead.
export interface EventStream {
  onopen: (() => void) | null;
  onmessage: Listener | null;
  onerror: (() => void) | null;
  addEventListener(type: string, listener: Listener): void;
  close(): void;
}

export function openEventStream(url: string): EventStream {
  if (!localSession()) {
    return new EventSource(url, { withCredentials: true }) as unknown as EventStream;
  }
  return new FetchEventStream(url);
}

export interface ParsedEvent {
  type: string;
  data: string | null;
  id?: string;
  retry?: number;
}

// parseEventBlock reads one server-sent event block. data is null for a
// block that carries no data, which still may set the id or retry delay.
export function parseEventBlock(block: string): ParsedEvent {
  const event: ParsedEvent = { type: "message", data: null };
  const data: string[] = [];
  for (const line of block.split("\n")) {
    if (line === "" || line.startsWith(":")) continue;
    const colon = line.indexOf(":");
    const field = colon < 0 ? line : line.slice(0, colon);
    let value = colon < 0 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "event") event.type = value;
    else if (field === "data") data.push(value);
    else if (field === "id" && !value.includes("\0")) event.id = value;
    else if (field === "retry" && /^\d+$/.test(value)) event.retry = Number(value);
  }
  if (data.length > 0) event.data = data.join("\n");
  return event;
}

const MAX_RETRY_MS = 30_000;

// FetchEventStream reads a server-sent event stream over fetch so it can
// send an Authorization header, and reconnects the way EventSource does: a
// dropped connection or a server error fires onerror, waits, and resumes
// with Last-Event-ID so the server replays only what was missed. A 4xx
// answer, like an EventSource's non-200, ends the stream for good.
export class FetchEventStream implements EventStream {
  onopen: (() => void) | null = null;
  onmessage: Listener | null = null;
  onerror: (() => void) | null = null;
  private listeners = new Map<string, Listener[]>();
  private abort = new AbortController();
  private lastEventId = "";
  private retryMs: number;
  private backoffMs: number;

  constructor(
    private readonly url: string,
    { initialRetryMs = 1_000 }: { initialRetryMs?: number } = {},
  ) {
    this.retryMs = initialRetryMs;
    this.backoffMs = initialRetryMs;
    void this.run();
  }

  addEventListener(type: string, listener: Listener): void {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }

  close(): void {
    this.abort.abort();
  }

  private get closed(): boolean {
    return this.abort.signal.aborted;
  }

  private dispatch(type: string, data: string) {
    const event = new MessageEvent(type, { data, lastEventId: this.lastEventId });
    if (type === "message") this.onmessage?.(event);
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }

  private async run() {
    await signedIn();
    while (!this.closed) {
      const fatal = await this.connect();
      if (this.closed) return;
      this.onerror?.();
      if (fatal || this.closed) return;
      await new Promise((resolve) => setTimeout(resolve, this.backoffMs));
      this.backoffMs = Math.min(this.backoffMs * 2, MAX_RETRY_MS);
    }
  }

  // connect reads one connection to its end and reports whether the
  // stream must not be retried.
  private async connect(): Promise<boolean> {
    try {
      const headers: Record<string, string> = { Accept: "text/event-stream", ...localAuthHeaders() };
      if (this.lastEventId) headers["Last-Event-ID"] = this.lastEventId;
      const res = await fetch(this.url, { headers, signal: this.abort.signal });
      if (res.status >= 400 && res.status < 500) return true;
      if (!res.ok || !res.body) return false;
      this.backoffMs = this.retryMs;
      this.onopen?.();
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffered = "";
      for (;;) {
        const { value, done } = await reader.read();
        if (done) return false;
        buffered += decoder.decode(value, { stream: true }).replace(/\r\n?/g, "\n");
        let end = buffered.indexOf("\n\n");
        while (end >= 0 && !this.closed) {
          const event = parseEventBlock(buffered.slice(0, end));
          buffered = buffered.slice(end + 2);
          if (event.id !== undefined) this.lastEventId = event.id;
          if (event.retry !== undefined) this.retryMs = this.backoffMs = event.retry;
          if (event.data !== null) this.dispatch(event.type, event.data);
          end = buffered.indexOf("\n\n");
        }
      }
    } catch {
      return false;
    }
  }
}
