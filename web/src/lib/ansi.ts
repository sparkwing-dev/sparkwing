
const SGR_RE = /\x1b\[([0-9;]*)m/g;

const CODE_TO_CLASS: Record<string, string> = {
  "1": "font-bold",
  "2": "opacity-60",
  "4": "underline",
  "30": "text-[#484f58]",
  "31": "text-red-400",
  "32": "text-green-400",
  "33": "text-yellow-400",
  "34": "text-blue-400",
  "35": "text-fuchsia-400",
  "36": "text-cyan-400",
  "37": "text-[#c9d1d9]",
  "90": "text-[#6e7681]",
  "91": "text-red-300",
  "92": "text-green-300",
  "93": "text-yellow-300",
  "94": "text-blue-300",
  "95": "text-fuchsia-300",
  "96": "text-cyan-300",
  "97": "text-white",
};

function escapeHTML(s: string): string {
  return s.replace(/[&<>"']/g, (ch) =>
    ch === "&"
      ? "&amp;"
      : ch === "<"
        ? "&lt;"
        : ch === ">"
          ? "&gt;"
          : ch === '"'
            ? "&quot;"
            : "&#39;",
  );
}

export function ansiToHtml(input: string): string {
  if (!input) return "";
  let out = "";
  let lastIndex = 0;
  let openSpans = 0;

  const flushText = (text: string) => {
    out += escapeHTML(text);
  };

  SGR_RE.lastIndex = 0;
  let match: RegExpExecArray | null;
  while ((match = SGR_RE.exec(input)) !== null) {
    flushText(input.slice(lastIndex, match.index));
    const codes = (match[1] || "0")
      .split(";")
      .map((c) => c.trim())
      .filter((c) => c !== "");

    for (const code of codes) {
      if (code === "0" || code === "") {
        while (openSpans > 0) {
          out += "</span>";
          openSpans--;
        }
        continue;
      }
      const cls = CODE_TO_CLASS[code];
      if (cls) {
        out += `<span class="${cls}">`;
        openSpans++;
      }
    }
    lastIndex = SGR_RE.lastIndex;
  }
  flushText(input.slice(lastIndex));
  while (openSpans > 0) {
    out += "</span>";
    openSpans--;
  }
  return out;
}

export function stripAnsi(s: string): string {
  return s.replace(SGR_RE, "");
}
