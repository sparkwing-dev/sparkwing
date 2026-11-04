"use client";

import { type RefObject, useEffect } from "react";

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

// trapTarget names where Tab should land instead of the browser's default,
// or null when the default already stays inside the trap.
export function trapTarget<T>(
  focusables: readonly T[],
  active: T | null,
  backwards: boolean,
): T | null {
  if (focusables.length === 0) return null;
  const first = focusables[0];
  const last = focusables[focusables.length - 1];
  const index = active === null ? -1 : focusables.indexOf(active);
  if (index === -1) return backwards ? last : first;
  if (backwards && index === 0) return last;
  if (!backwards && index === focusables.length - 1) return first;
  return null;
}

// useFocusTrap keeps keyboard focus inside a modal while it is open and
// hands it back to whatever opened the modal once it closes.
export function useFocusTrap(
  ref: RefObject<HTMLElement | null>,
  active: boolean,
) {
  useEffect(() => {
    const container = ref.current;
    if (!active || !container) return;
    const opener =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
    const focusables = () =>
      Array.from(container.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
        (el) => el.getClientRects().length > 0,
      );
    (focusables()[0] ?? container).focus();
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Tab") return;
      const current =
        document.activeElement instanceof HTMLElement &&
        container.contains(document.activeElement)
          ? document.activeElement
          : null;
      const target = trapTarget(focusables(), current, event.shiftKey);
      if (target) {
        event.preventDefault();
        target.focus();
      } else if (!current) {
        event.preventDefault();
        container.focus();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      if (opener?.isConnected) opener.focus();
    };
  }, [ref, active]);
}
