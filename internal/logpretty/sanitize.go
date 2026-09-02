package logpretty

import "strings"

// StripANSI removes every escape sequence and every C0 control byte other than tab from s.
// Plain-text log output keeps one record on one line, so newlines are removed as well.
func StripANSI(s string) string { return sanitize(s, false) }

// SanitizeANSI removes every escape sequence except SGR codes on the allow-list shared with the
// web log viewer, and every C0 control byte other than tab, from s.
func SanitizeANSI(s string) string { return sanitize(s, true) }

var allowedSGRCodes = map[string]bool{
	"0": true, "1": true, "2": true, "4": true,
	"30": true, "31": true, "32": true, "33": true, "34": true, "35": true, "36": true, "37": true,
	"90": true, "91": true, "92": true, "93": true, "94": true, "95": true, "96": true, "97": true,
}

func sanitize(s string, keepSGR bool) string {
	if !needsSanitizing(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == escByte {
			seq, next := scanEscape(s, i)
			if keepSGR {
				if kept, ok := allowedSGR(seq); ok {
					b.WriteString(kept)
				}
			}
			i = next
			continue
		}
		if isControlByte(c) {
			i++
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

const escByte = 0x1b

func isControlByte(c byte) bool { return (c < 0x20 && c != '\t') || c == 0x7f }

func needsSanitizing(s string) bool {
	for i := 0; i < len(s); i++ {
		if isControlByte(s[i]) {
			return true
		}
	}
	return false
}

// scanEscape returns the escape sequence starting at i and the index just past it. A sequence
// truncated by the end of the input consumes the rest of the input.
func scanEscape(s string, i int) (string, int) {
	j := i + 1
	if j >= len(s) {
		return s[i:], len(s)
	}
	switch c := s[j]; {
	case c == '[':
		j++
		for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
			j++
		}
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
			j++
		}
		return s[i:j], j
	case c == ']' || c == 'P' || c == 'X' || c == '^' || c == '_':
		j++
		for j < len(s) {
			if s[j] == 0x07 {
				j++
				break
			}
			if s[j] == escByte && j+1 < len(s) && s[j+1] == '\\' {
				j += 2
				break
			}
			j++
		}
		return s[i:j], j
	case c >= 0x20 && c <= 0x2f:
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		if j < len(s) && s[j] >= 0x30 && s[j] <= 0x7e {
			j++
		}
		return s[i:j], j
	default:
		return s[i : j+1], j + 1
	}
}

// allowedSGR reports whether seq is an SGR sequence and returns it reduced to its allow-listed codes.
func allowedSGR(seq string) (string, bool) {
	if len(seq) < 3 || seq[1] != '[' || seq[len(seq)-1] != 'm' {
		return "", false
	}
	params := seq[2 : len(seq)-1]
	if params == "" {
		return "\x1b[0m", true
	}
	kept := make([]string, 0, 4)
	for _, code := range strings.Split(params, ";") {
		if allowedSGRCodes[code] {
			kept = append(kept, code)
		}
	}
	if len(kept) == 0 {
		return "", false
	}
	return "\x1b[" + strings.Join(kept, ";") + "m", true
}
