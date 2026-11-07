type Listener = (e: MessageEvent) => void;

export interface EventStream {
  onopen: (() => void) | null;
  onmessage: Listener | null;
  onerror: (() => void) | null;
  addEventListener(type: string, listener: Listener): void;
  close(): void;
}

// Local and team dashboards both authenticate streams with cookies.
export function openEventStream(url: string): EventStream {
  return new EventSource(url, { withCredentials: true }) as unknown as EventStream;
}
