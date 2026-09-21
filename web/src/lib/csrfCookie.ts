// The dashboard names its CSRF cookie `__Host-sw_csrf` wherever it keeps the
// Secure attribute, and falls back to the bare name on the local-http escape,
// because a browser refuses a __Host- cookie that is not Secure. The prefixed
// name is read first because a sibling host under the registrable domain can
// write the bare one and cannot write the prefixed one.
const csrfCookieNames = ["__Host-sw_csrf", "sw_csrf"];

export function readCSRFCookie(): string {
  if (typeof document === "undefined") return "";
  const entries = document.cookie.split(";").map((value) => value.trim());
  for (const name of csrfCookieNames) {
    const prefix = `${name}=`;
    const entry = entries.find((value) => value.startsWith(prefix));
    if (entry === undefined) continue;
    try {
      return decodeURIComponent(entry.slice(prefix.length));
    } catch {
      return "";
    }
  }
  return "";
}
