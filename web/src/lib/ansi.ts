
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

function normalizeCode(param: string): string {
  const trimmed = param.replace(/^0+/, "");
  return trimmed === "" ? "0" : trimmed;
}

// Splits SGR parameters into codes, folding a 38 or 48 extended colour selector and its arguments
// into the single code they belong to. Returns null when that selector is malformed.
function sgrCodes(params: string): string[] | null {
  const raw = params.split(";");
  const out: string[] = [];
  for (let i = 0; i < raw.length; i++) {
    const code = normalizeCode(raw[i]);
    if (code !== "38" && code !== "48") {
      out.push(code);
      continue;
    }
    const kind = i + 1 < raw.length ? normalizeCode(raw[i + 1]) : "";
    const want = kind === "5" ? 1 : kind === "2" ? 3 : -1;
    if (want < 0 || i + 1 + want >= raw.length) return null;
    out.push(
      [code, ...raw.slice(i + 1, i + 2 + want).map(normalizeCode)].join(";"),
    );
    i += 1 + want;
  }
  return out;
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
    lastIndex = SGR_RE.lastIndex;
    const codes = sgrCodes(match[1] ?? "");
    // One unrecognized parameter drops the whole sequence, so a compound SGR never degrades into
    // attributes its producer never asked for.
    if (codes === null || codes.some((c) => c !== "0" && !CODE_TO_CLASS[c])) {
      continue;
    }

    for (const code of codes) {
      if (code === "0") {
        while (openSpans > 0) {
          out += "</span>";
          openSpans--;
        }
        continue;
      }
      out += `<span class="${CODE_TO_CLASS[code]}">`;
      openSpans++;
    }
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
