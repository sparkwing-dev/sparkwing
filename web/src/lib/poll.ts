// startPoll runs task now and then every intervalMs while the tab is visible.
// A tick that arrives while the previous run is still pending is skipped, so a
// slow controller answers one request at a time and that answer still renders.
// The task reports its own errors; a rejection only ends that run.
export function startPoll(
  task: () => Promise<unknown>,
  intervalMs: number,
): () => void {
  let stopped = false;
  let inFlight = false;
  const run = () => {
    if (stopped || inFlight) return;
    inFlight = true;
    task()
      .catch(() => {})
      .finally(() => {
        inFlight = false;
      });
  };
  queueMicrotask(run);
  const interval = setInterval(() => {
    if (!document.hidden) run();
  }, intervalMs);
  return () => {
    stopped = true;
    clearInterval(interval);
  };
}
