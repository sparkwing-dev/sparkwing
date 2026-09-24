"use client";

import Tooltip from "./Tooltip";

export interface RailItem {
  id: string;
  label: string;
  dotClass: string;
}

export default function RunRail({
  kind,
  items,
  selectedID,
  onSelect,
}: {
  kind: "runs" | "nodes";
  items: RailItem[];
  selectedID: string | null;
  onSelect: (id: string) => void;
}) {
  return (
    <div aria-label={`${kind === "runs" ? "Runs" : "Nodes"} rail`} className="flex flex-col items-center">
      {items.map((item) => (
        <Tooltip key={item.id} content={item.label}>
          <button
            type="button"
            data-rail-id={item.id}
            aria-pressed={selectedID === item.id}
            aria-label={item.label}
            onClick={() => onSelect(item.id)}
            className="w-7 h-8 flex items-center justify-center rounded hover:bg-[var(--surface-raised)] focus-visible:outline-2 focus-visible:outline-violet-300"
          >
            <span
              aria-hidden="true"
              className={`w-2.5 h-2.5 rounded-full ${item.dotClass} ${selectedID === item.id ? "ring-2 ring-offset-2 ring-offset-[var(--surface)] ring-violet-300" : ""}`}
            />
          </button>
        </Tooltip>
      ))}
    </div>
  );
}
