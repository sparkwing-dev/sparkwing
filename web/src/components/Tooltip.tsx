"use client";

import { useState, useRef, useEffect, type ReactNode } from "react";

interface TooltipProps {
  content: ReactNode;
  children: ReactNode;
}

export default function Tooltip({ content, children }: TooltipProps) {
  const [show, setShow] = useState(false);
  const [pos, setPos] = useState<{
    x: number;
    y: number;
    align: "center" | "left" | "right";
    flip: boolean;
  }>({ x: 0, y: 0, align: "center", flip: false });
  const ref = useRef<HTMLSpanElement>(null);
  const tipRef = useRef<HTMLDivElement>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    if (!show || !ref.current) return;
    const rect = ref.current.getBoundingClientRect();
    const tipWidth = tipRef.current?.offsetWidth || 200;
    const tipHeight = tipRef.current?.offsetHeight || 60;
    const cx = rect.left + rect.width / 2;
    // Flip below the trigger when there isn't enough room above for
    // the tooltip + arrow + a few pixels of breathing room.
    const flip = rect.top - tipHeight - 12 < 8;
    const y = flip ? rect.bottom + 6 : rect.top - 6;
    let x = cx;
    let align: "center" | "left" | "right" = "center";
    if (cx - tipWidth / 2 < 8) {
      x = rect.left;
      align = "left";
    } else if (cx + tipWidth / 2 > window.innerWidth - 8) {
      x = rect.right;
      align = "right";
    }
    setPos({ x, y, align, flip });
  }, [show]);

  const handleMouseEnter = () => {
    timerRef.current = setTimeout(() => setShow(true), 500);
  };

  const handleMouseLeave = () => {
    if (timerRef.current) clearTimeout(timerRef.current);
    setShow(false);
  };

  const xTransform =
    pos.align === "left" ? "0" : pos.align === "right" ? "-100%" : "-50%";
  const yTransform = pos.flip ? "0" : "-100%";
  const transform = `translate(${xTransform}, ${yTransform})`;

  const arrowAlign =
    pos.align === "left"
      ? "ml-3"
      : pos.align === "right"
        ? "mr-3 ml-auto"
        : "mx-auto";
  // When flipped, the arrow sits above the bubble pointing up; with
  // the default direction it sits below pointing down.
  const arrow = (
    <div
      className={`w-2 h-2 bg-[#1e293b] border-${pos.flip ? "t" : "b"} border-${pos.flip ? "l" : "r"} border-[var(--border)] rotate-45 ${pos.flip ? "-mb-1" : "-mt-1"} ${arrowAlign}`}
    />
  );

  return (
    <>
      <span
        ref={ref}
        onMouseEnter={handleMouseEnter}
        onMouseLeave={handleMouseLeave}
      >
        {children}
      </span>
      {show && (
        <div
          ref={tipRef}
          className="fixed z-[100] pointer-events-none"
          style={{ left: pos.x, top: pos.y, transform }}
        >
          {pos.flip && arrow}
          <div className="bg-[#1e293b] border border-[var(--border)] rounded-lg px-3 py-2 text-xs shadow-xl whitespace-pre-wrap break-words max-w-[min(90vw,40rem)] w-max">
            {content}
          </div>
          {!pos.flip && arrow}
        </div>
      )}
    </>
  );
}
