import { completenessLine, type LogCompleteness } from "@/lib/logCompleteness";

// LogCompletenessLine draws the reader's note after a node's log. It sits
// outside the log's text, so selecting and copying the log leaves it out.
export default function LogCompletenessLine({
  completeness,
}: {
  completeness: LogCompleteness | null;
}) {
  const line = completenessLine(completeness);
  if (!line || !completeness) return null;
  const tone =
    completeness.state === "unconfirmed"
      ? "border-yellow-500/40 bg-yellow-500/10 text-yellow-300"
      : "border-red-500/40 bg-red-500/10 text-red-300";
  return (
    <div
      role="status"
      data-log-completeness={completeness.state}
      className={`mt-2 select-none rounded border border-dashed px-3 py-1.5 text-center font-mono text-xs italic ${tone}`}
    >
      {line}
    </div>
  );
}
