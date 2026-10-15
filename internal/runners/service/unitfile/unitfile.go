// Package unitfile shares native unit argument encoding and inspection.
package unitfile

import (
	"bytes"
	"encoding/xml"
	"io"
	"strconv"
	"strings"
)

// PlistArguments reads one complete ProgramArguments array, refusing duplicates and malformed XML.
func PlistArguments(body []byte) []string {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	depth, roots, found := 0, 0, 0
	want := false
	var args []string
	for {
		token, err := decoder.Token()
		if err == io.EOF && depth == 0 && roots == 1 && found == 1 && !want {
			return args
		}
		if err != nil {
			return nil
		}
		switch token := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots != 1 || token.Name.Local != "plist" {
					return nil
				}
			}
			if depth == 2 && token.Name.Local == "key" {
				var key string
				if decoder.DecodeElement(&key, &token) != nil || want {
					return nil
				}
				want = key == "ProgramArguments"
				continue
			}
			if depth == 2 && want {
				if token.Name.Local != "array" || found != 0 {
					return nil
				}
				var ok bool
				args, ok = plistStrings(decoder)
				if !ok {
					return nil
				}
				found++
				want = false
				continue
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
}

func plistStrings(decoder *xml.Decoder) ([]string, bool) {
	var args []string
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, false
		}
		switch token := token.(type) {
		case xml.StartElement:
			if token.Name.Local != "string" {
				return nil, false
			}
			var arg string
			if decoder.DecodeElement(&arg, &token) != nil {
				return nil, false
			}
			args = append(args, arg)
		case xml.EndElement:
			return args, token.Name.Local == "array"
		case xml.CharData:
			if strings.TrimSpace(string(token)) != "" {
				return nil, false
			}
		}
	}
}

// SystemdWords decodes literal native argv, refusing incomplete quotes and invalid escapes.
func SystemdWords(s string) []string {
	var words []string
	var word strings.Builder
	var quote byte
	started := false
	appendWord := func() {
		words = append(words, strings.ReplaceAll(word.String(), "%%", "%"))
		word.Reset()
		started = false
	}
	for len(s) > 0 {
		c := s[0]
		s = s[1:]
		switch {
		case c == '\\':
			if len(s) == 0 {
				return nil
			}
			if s[0] == 's' {
				word.WriteByte(' ')
				s = s[1:]
			} else {
				value, _, rest, err := strconv.UnquoteChar("\\"+s, '"')
				if err != nil {
					return nil
				}
				word.WriteRune(value)
				s = rest
			}
			started = true
		case c == '"' || c == '\'':
			if quote == 0 {
				quote = c
			} else if quote == c {
				quote = 0
			} else {
				word.WriteByte(c)
			}
			started = true
		case quote == 0 && (c == ' ' || c == '\t'):
			if started {
				appendWord()
			}
		default:
			word.WriteByte(c)
			started = true
		}
	}
	if quote != 0 {
		return nil
	}
	if started {
		appendWord()
	}
	return words
}

// SystemdWord quotes native syntax and specifiers. Command disables variable expansion.
func SystemdWord(v string) string {
	escaped := strings.ReplaceAll(v, "%", "%%")
	return quoteWord(escaped)
}

// Assignment encodes an Environment directive, where dollar signs are literal.
func Assignment(key, value string) string {
	return quoteWord(strings.ReplaceAll(key+"="+value, "%", "%%"))
}

func quoteWord(escaped string) string {
	if escaped != "" && !strings.ContainsAny(escaped, " \t\r\n\x00\"'\\") {
		return escaped
	}
	return strconv.Quote(escaped)
}

// XMLText escapes native plist text.
func XMLText(s string) string {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(s)); err != nil {
		return ""
	}
	return buf.String()
}
