import { useSyncExternalStore } from "react";
import { readCSRFCookie } from "./csrfCookie";

function subscribe() {
  return () => {};
}

// The controller's own sign-out form needs the session's CSRF token; a console
// served without required sign-in has no session to end, so it reads empty.
function read() {
  const configured = (
    window as unknown as { __SPARKWING_REQUIRE_LOGIN__?: string }
  ).__SPARKWING_REQUIRE_LOGIN__;
  if (configured !== "true") return "";
  return readCSRFCookie();
}

function empty() {
  return "";
}

export function useLogoutCSRFToken(): string {
  return useSyncExternalStore(subscribe, read, empty);
}
