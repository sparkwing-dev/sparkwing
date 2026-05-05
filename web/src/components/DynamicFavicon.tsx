"use client";

import { useEffect } from "react";

// Localhost loopbacks (laptop `sparkwing dev` + port-forward) keep the
// default blue favicon; anything else is treated as a deployed cluster
// and swapped to orange so it's visually distinct from a local tab.
const LOCAL_HOSTNAMES = new Set([
  "localhost",
  "127.0.0.1",
  "::1",
  "[::1]",
  "0.0.0.0",
]);

export default function DynamicFavicon() {
  useEffect(() => {
    if (LOCAL_HOSTNAMES.has(window.location.hostname)) return;

    document
      .querySelectorAll('link[rel="icon"], link[rel="shortcut icon"]')
      .forEach((el) => el.remove());

    const link = document.createElement("link");
    link.rel = "icon";
    link.href = "/favicon-orange.ico";
    document.head.appendChild(link);
  }, []);

  return null;
}
