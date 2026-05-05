"use client";

import { useState, useRef, useEffect, type ReactNode } from "react";

interface TooltipProps {
  content: ReactNode;
  children: ReactNode;
}

export default function Tooltip({ content, children }: TooltipProps) {
  const [show, setShow] = useState(false);
  const [pos, setPos] = useState<{ x: number; y: number; align: "center" | "left" | "right" }>({ x: 0, y: 0, align: "center" });
  const ref = useRef<HTMLSpanElement>(null);
  const tipRef = useRef<HTMLDivElement>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    if (!show || !ref.current) return;
    const rect = ref.current.getBoundingClientRect();
    const tipWidth = tipRef.current?.offsetWidth || 200;
    const cx = rect.left + rect.width / 2;

    // If centered tooltip would clip left edge, anchor to left
    if (cx - tipWidth / 2 < 8) {
      setPos({ x: rect.left, y: rect.top - 6, align: "left" });
    // If it would clip right edge, anchor to right
    } else if (cx + tipWidth / 2 > window.innerWidth - 8) {
      setPos({ x: rect.right, y: rect.top - 6, align: "right" });
    } else {
      setPos({ x: cx, y: rect.top - 6, align: "center" });
    }
  }, [show]);

  const handleMouseEnter = () => {
    timerRef.current = setTimeout(() => setShow(true), 500);
  };

  const handleMouseLeave = () => {
    if (timerRef.current) clearTimeout(timerRef.current);
    setShow(false);
  };

  const transform = pos.align === "left"
    ? "translate(0, -100%)"
    : pos.align === "right"
      ? "translate(-100%, -100%)"
      : "translate(-50%, -100%)";

  const arrowAlign = pos.align === "left"
    ? "ml-3"
    : pos.align === "right"
      ? "mr-3 ml-auto"
      : "mx-auto";

  return (
    <>
      <span
        ref={ref}
        onMouseEnter={handleMouseEnter}
        onMouseLeave={handleMouseLeave}
        className="inline-flex items-center"
      >
        {children}
      </span>
      {show && (
        <div
          ref={tipRef}
          className="fixed z-[100] pointer-events-none"
          style={{ left: pos.x, top: pos.y, transform }}
        >
          <div className="bg-[#1e293b] border border-[var(--border)] rounded-lg px-3 py-2 text-xs shadow-xl max-w-xs">
            {content}
          </div>
          <div className={`w-2 h-2 bg-[#1e293b] border-b border-r border-[var(--border)] rotate-45 -mt-1 ${arrowAlign}`} />
        </div>
      )}
    </>
  );
}
