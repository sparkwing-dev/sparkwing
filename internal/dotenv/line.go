// Package dotenv decodes the line-oriented format used by file secret sources.
package dotenv

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseLine returns an empty key for blank lines and comments. Values support
// Go-quoted strings, literal single quotes, and whitespace-separated comments.
func ParseLine(line string) (string, string, error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", nil
	}
	if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "export\t") {
		line = strings.TrimSpace(line[6:])
	}
	eq := strings.IndexByte(line, '=')
	if eq <= 0 {
		return "", "", fmt.Errorf("malformed line, want KEY=VALUE")
	}
	key := strings.TrimSpace(line[:eq])
	rawValue := line[eq+1:]
	leadingSpace := strings.HasPrefix(rawValue, " ") || strings.HasPrefix(rawValue, "\t")
	value := strings.TrimSpace(rawValue)
	var quote byte
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if quote != 0 {
			if ch == '\\' && quote == '"' {
				i++
				continue
			}
			if ch == quote {
				quote = 0
			}
		} else if i == 0 && (ch == '"' || ch == '\'') {
			quote = ch
		} else if ch == '#' && ((i == 0 && leadingSpace) || (i > 0 && (value[i-1] == ' ' || value[i-1] == '\t'))) {
			value = strings.TrimSpace(value[:i])
			break
		}
	}
	if len(value) >= 2 {
		if value[0] == '"' && value[len(value)-1] == '"' {
			if decoded, err := strconv.Unquote(value); err == nil {
				return key, decoded, nil
			}
			return key, value[1 : len(value)-1], nil
		}
		if value[0] == '\'' && value[len(value)-1] == '\'' {
			return key, value[1 : len(value)-1], nil
		}
	}
	return key, value, nil
}
