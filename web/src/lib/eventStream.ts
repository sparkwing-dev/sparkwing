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

// parseEventBlock reads one server-sent event: its type and its data lines.
export function parseEventBlock(block: string): { type: string; data: string } | null {
  let type = "message";
  const data: string[] = [];
  for (const line of block.split("\n")) {
    if (line === "" || line.startsWith(":")) continue;
    const colon = line.indexOf(":");
    const field = colon < 0 ? line : line.slice(0, colon);
    let value = colon < 0 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "event") type = value;
    else if (field === "data") data.push(value);
  }
  return data.length === 0 ? null : { type, data: data.join("\n") };
}

class FetchEventStream implements EventStream {
  onopen: (() => void) | null = null;
  onmessage: Listener | null = null;
  onerror: (() => void) | null = null;
  private listeners = new Map<string, Listener[]>();
  private abort = new AbortController();

  constructor(url: string) {
    void this.run(url);
  }

  addEventListener(type: string, listener: Listener): void {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }

  close(): void {
    this.abort.abort();
  }

  private dispatch(type: string, data: string) {
    const event = new MessageEvent(type, { data });
    if (type === "message") this.onmessage?.(event);
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }

  private async run(url: string) {
    try {
      await signedIn();
      const res = await fetch(url, {
        headers: { Accept: "text/event-stream", ...localAuthHeaders() },
        signal: this.abort.signal,
      });
      if (!res.ok || !res.body) throw new Error(`stream answered ${res.status}`);
      this.onopen?.();
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffered = "";
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buffered += decoder.decode(value, { stream: true }).replace(/\r\n?/g, "\n");
        let end = buffered.indexOf("\n\n");
        while (end >= 0) {
          const event = parseEventBlock(buffered.slice(0, end));
          buffered = buffered.slice(end + 2);
          if (event) this.dispatch(event.type, event.data);
          end = buffered.indexOf("\n\n");
        }
      }
      if (!this.abort.signal.aborted) this.onerror?.();
    } catch {
      if (!this.abort.signal.aborted) this.onerror?.();
    }
  }
}
