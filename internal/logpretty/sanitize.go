package logpretty

import (
	"strings"
	"unicode/utf8"
)

// StripANSI removes every escape sequence and every control character other than tab and newline
// from s, including the C1 range U+0080 through U+009F in both its raw and its UTF-8 form.
func StripANSI(s string) string { return sanitize(s, false) }

// SanitizeANSI removes every escape sequence except SGR codes on the allow-list shared with the
// web log viewer, and every control character other than tab and newline, from s.
func SanitizeANSI(s string) string { return sanitize(s, true) }

// SanitizeInline is SanitizeANSI with newlines folded to spaces, for a value rendered inside a
// line such as a node id, a step name, or a skip reason.
func SanitizeInline(s string) string { return foldLines(SanitizeANSI(s)) }

// StripInline is StripANSI with newlines folded to spaces, for a value rendered inside a line.
func StripInline(s string) string { return foldLines(StripANSI(s)) }

var allowedSGRCodes = map[string]bool{
	"0": true, "1": true, "2": true, "4": true,
	"30": true, "31": true, "32": true, "33": true, "34": true, "35": true, "36": true, "37": true,
	"90": true, "91": true, "92": true, "93": true, "94": true, "95": true, "96": true, "97": true,
}

const (
	escByte    = 0x1b
	c1Lo       = 0x80
	c1Hi       = 0x9f
	c1UTF8Lead = 0xc2
)

const (
	c1DCS = 0x90
	c1SOS = 0x98
	c1CSI = 0x9b
	c1ST  = 0x9c
	c1OSC = 0x9d
	c1PM  = 0x9e
	c1APC = 0x9f
)

func foldLines(s string) string { return strings.ReplaceAll(s, "\n", " ") }

func sanitize(s string, keepSGR bool) string {
	if !needsSanitizing(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == escByte:
			seq, next := scanEscape(s, i)
			if keepSGR {
				if kept, ok := allowedSGR(seq); ok {
					b.WriteString(kept)
				}
			}
			i = next
		case c < utf8.RuneSelf:
			if !isC0(c) {
				b.WriteByte(c)
			}
			i++
		case isC1(c):
			i = scanC1(s, i, 1, c)
		case c == c1UTF8Lead && i+1 < len(s) && isC1(s[i+1]):
			i = scanC1(s, i, 2, s[i+1])
		default:
			_, size := utf8.DecodeRuneInString(s[i:])
			b.WriteString(s[i : i+size])
			i += size
		}
	}
	return b.String()
}

func isC0(c byte) bool { return (c < 0x20 && c != '\t' && c != '\n') || c == 0x7f }

func isC1(c byte) bool { return c >= c1Lo && c <= c1Hi }

// safety: 0xc2 leads every two-byte encoding of the C1 range, which a UTF-8 terminal decodes to
// the same controls as the raw bytes, so a string carrying one still has to walk the loop.
func needsSanitizing(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; isC0(c) || isC1(c) || c == c1UTF8Lead {
			return true
		}
	}
	return false
}

func scanEscape(s string, i int) (string, int) {
	j := i + 1
	if j >= len(s) {
		return s[i:], len(s)
	}
	switch c := s[j]; {
	case c == '[':
		end := scanCSI(s, j+1)
		return s[i:end], end
	case c == ']' || c == 'P' || c == 'X' || c == '^' || c == '_':
		end := scanString(s, j+1)
		return s[i:end], end
	case c >= 0x20 && c <= 0x2f:
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		if j < len(s) && s[j] >= 0x30 && s[j] <= 0x7e {
			j++
		}
		return s[i:j], j
	default:
		_, size := utf8.DecodeRuneInString(s[j:])
		return s[i : j+size], j + size
	}
}

func scanC1(s string, i, introWidth int, code byte) int {
	afterIntro := i + introWidth
	switch code {
	case c1CSI:
		return scanCSI(s, afterIntro)
	case c1OSC, c1DCS, c1SOS, c1PM, c1APC:
		return scanString(s, afterIntro)
	default:
		return afterIntro
	}
}

func scanCSI(s string, afterIntro int) int {
	j := afterIntro
	for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
		j++
	}
	for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
		j++
	}
	if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
		j++
	}
	return j
}

// safety: the scan stops at the first byte outside the string-parameter range, so a truncated
// control string consumes only itself and cannot hide the error text that follows it.
func scanString(s string, afterIntro int) int {
	j := afterIntro
	for j < len(s) {
		switch c := s[j]; {
		case c == 0x07 || c == c1ST:
			return j + 1
		case c == escByte:
			if j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
			return j
		case c == c1UTF8Lead && j+1 < len(s) && s[j+1] == c1ST:
			return j + 2
		case c >= 0x20 && c <= 0x7e:
			j++
		default:
			return j
		}
	}
	return j
}

// safety: one unrecognized parameter drops the whole sequence, so a compound SGR never degrades
// into attributes its producer never asked for.
func allowedSGR(seq string) (string, bool) {
	if len(seq) < 3 || seq[1] != '[' || seq[len(seq)-1] != 'm' {
		return "", false
	}
	params := seq[2 : len(seq)-1]
	if params == "" {
		return "\x1b[0m", true
	}
	codes, ok := sgrParams(params)
	if !ok {
		return "", false
	}
	for _, code := range codes {
		if !allowedSGRCodes[code] {
			return "", false
		}
	}
	return "\x1b[" + strings.Join(codes, ";") + "m", true
}

func sgrParams(params string) ([]string, bool) {
	raw := strings.Split(params, ";")
	out := make([]string, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		code, ok := sgrCode(raw[i])
		if !ok {
			return nil, false
		}
		if code != "38" && code != "48" {
			out = append(out, code)
			continue
		}
		args, ok := extendedColorArgs(raw, i+1)
		if !ok {
			return nil, false
		}
		out = append(out, strings.Join(append([]string{code}, args...), ";"))
		i += len(args)
	}
	return out, true
}

func extendedColorArgs(raw []string, i int) ([]string, bool) {
	if i >= len(raw) {
		return nil, false
	}
	kind, ok := sgrCode(raw[i])
	if !ok {
		return nil, false
	}
	want := 0
	switch kind {
	case "5":
		want = 1
	case "2":
		want = 3
	default:
		return nil, false
	}
	if i+want >= len(raw) {
		return nil, false
	}
	args := make([]string, 0, want+1)
	args = append(args, kind)
	for _, p := range raw[i+1 : i+1+want] {
		code, ok := sgrCode(p)
		if !ok {
			return nil, false
		}
		args = append(args, code)
	}
	return args, true
}

func sgrCode(p string) (string, bool) {
	for i := 0; i < len(p); i++ {
		if p[i] < '0' || p[i] > '9' {
			return "", false
		}
	}
	trimmed := strings.TrimLeft(p, "0")
	if trimmed == "" {
		return "0", true
	}
	return trimmed, true
}
