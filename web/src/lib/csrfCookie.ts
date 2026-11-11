export function readCSRFCookie(): string {
  if (typeof document === "undefined") return "";
  const prefix = "__Host-sw_csrf=";
  const entry = document.cookie
    .split(";")
    .map((value) => value.trim())
    .find((value) => value.startsWith(prefix));
  if (entry === undefined) return "";
  try {
    return decodeURIComponent(entry.slice(prefix.length));
  } catch {
    return "";
  }
}
