// RunsListPlaceholder fills an empty runs list. Until the current view's first
// response arrives it shows skeleton rows, because an empty list then means
// "not answered yet" rather than "no runs".
export default function RunsListPlaceholder({
  loading,
  failed,
  emptyText,
}: {
  loading: boolean;
  failed: boolean;
  emptyText: string;
}) {
  if (failed) return null;
  if (loading) {
    return (
      <div
        role="status"
        aria-label="Loading runs"
        className="animate-pulse motion-reduce:animate-none"
      >
        {Array.from({ length: 6 }, (_, index) => (
          <div
            key={index}
            className="h-14 px-3 py-2 border-b border-[var(--border)] border-l-4 border-l-transparent flex flex-col justify-center gap-2"
          >
            <div className="h-3 w-3/5 max-w-md rounded bg-[var(--surface-raised)]" />
            <div className="h-2.5 w-2/5 max-w-xs rounded bg-[var(--surface)]" />
          </div>
        ))}
      </div>
    );
  }
  return (
    <div className="p-8 text-center text-[var(--muted)] text-sm">
      {emptyText}
    </div>
  );
}
