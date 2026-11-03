"use client";

import type { NodeWorkStep } from "@/lib/api";
import {
  STEP_STATE_COLOR,
  layoutStepProgress,
  stepIndexAt,
  stepProgressLabel,
  stepState,
} from "@/lib/stepProgress";

// StepProgressBar draws a job's steps as a thin bar inside an SVG, one
// segment per step in step order. It occupies exactly the box it is given,
// so state changes recolor it without moving anything around it.
export default function StepProgressBar({
  steps,
  x,
  y,
  width,
  height,
  onTip,
}: {
  steps: NodeWorkStep[];
  x: number;
  y: number;
  width: number;
  height: number;
  onTip?: (tip: { text: string; x: number; y: number } | null) => void;
}) {
  const states = steps.map((s) => stepState(s.status));
  const layout = layoutStepProgress(states, width);
  const tipAt = (e: React.MouseEvent<SVGRectElement>) => {
    if (!onTip) return;
    const box = e.currentTarget.getBoundingClientRect();
    const local =
      box.width > 0 ? ((e.clientX - box.left) * width) / box.width : 0;
    const i = stepIndexAt(layout, local, steps.length);
    if (i < 0) return;
    onTip({
      text: `${steps[i].id} · ${states[i]}`,
      x: e.clientX,
      y: e.clientY,
    });
  };
  return (
    <g
      role="img"
      aria-label={stepProgressLabel(states)}
      data-testid="step-progress"
      data-mode={layout.mode}
      transform={`translate(${x}, ${y})`}
    >
      {layout.rects.map((r) =>
        layout.mode === "segments" ? (
          <rect
            key={r.first}
            data-step-segment={steps[r.first].id}
            data-step-state={r.state}
            x={r.x}
            width={r.w}
            height={height}
            rx={Math.min(1, height / 2)}
            fill={STEP_STATE_COLOR[r.state]}
          />
        ) : (
          <rect
            key={r.first}
            data-step-run={r.count}
            data-step-state={r.state}
            x={r.x}
            width={r.w}
            height={height}
            fill={STEP_STATE_COLOR[r.state]}
          />
        ),
      )}
      {onTip && (
        <rect
          data-testid="step-progress-hit"
          y={-4}
          width={width}
          height={height + 6}
          fill="transparent"
          onMouseEnter={tipAt}
          onMouseMove={tipAt}
          onMouseLeave={() => onTip(null)}
        />
      )}
    </g>
  );
}
