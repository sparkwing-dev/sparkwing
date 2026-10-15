// Package unitfile shares native unit argument encoding and inspection.
package unitfile

import (
	"bytes"
	"encoding/xml"
	"io"
	"strconv"
	"strings"
)

type plistElement struct {
	XMLName  xml.Name
	Text     string         `xml:",chardata"`
	Elements []plistElement `xml:",any"`
}

// PlistArguments reads one complete ProgramArguments array, refusing duplicates and malformed XML.
func PlistArguments(body []byte) []string {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	var root plistElement
	if decoder.Decode(&root) != nil || decoder.Decode(new(plistElement)) != io.EOF ||
		root.XMLName.Local != "plist" || len(root.Elements) != 1 || root.Elements[0].XMLName.Local != "dict" {
		return nil
	}
	entries := root.Elements[0].Elements
	if len(entries)%2 != 0 {
		return nil
	}
	var args []string
	found := false
	for i := 0; i < len(entries); i += 2 {
		key, value := entries[i], entries[i+1]
		if key.XMLName.Local != "key" || len(key.Elements) != 0 {
			return nil
		}
		if key.Text != "ProgramArguments" {
			continue
		}
		if found || value.XMLName.Local != "array" || strings.TrimSpace(value.Text) != "" {
			return nil
		}
		found = true
		for _, argument := range value.Elements {
			if argument.XMLName.Local != "string" || len(argument.Elements) != 0 {
				return nil
			}
			args = append(args, argument.Text)
		}
	}
	return args
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
