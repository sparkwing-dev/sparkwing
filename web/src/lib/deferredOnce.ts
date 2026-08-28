export type DeferredMarker<T> = { current: T | null };
export type DeferredScheduler = (callback: () => void) => () => void;

export function deferOnce<T>(
  marker: DeferredMarker<T>,
  key: T,
  schedule: DeferredScheduler,
  action: () => boolean,
): () => void {
  if (marker.current === key) return () => {};
  let active = true;
  const cancel = schedule(() => {
    if (!active) return;
    if (action()) marker.current = key;
  });
  return () => {
    active = false;
    cancel();
  };
}

export function scheduleMicrotask(callback: () => void): () => void {
  let active = true;
  queueMicrotask(() => {
    if (active) callback();
  });
  return () => {
    active = false;
  };
}

export function scheduleAnimationFrame(callback: () => void): () => void {
  let active = true;
  const frame = requestAnimationFrame(() => {
    if (active) callback();
  });
  return () => {
    active = false;
    cancelAnimationFrame(frame);
  };
}
