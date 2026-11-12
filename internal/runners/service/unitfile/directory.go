package unitfile

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// RewriteWorkingDirectory updates only the native working directory, retaining captured settings.
func RewriteWorkingDirectory(goos, body, directory string) (string, error) {
	if !filepath.IsAbs(directory) || strings.ContainsRune(directory, 0) {
		return "", errors.New("native unit: working directory must be an absolute path without NUL bytes")
	}
	if goos == "linux" {
		lines := strings.Split(body, "\n")
		section, header, found := false, -1, false
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[") {
				section = trimmed == "[Service]"
				if section && header < 0 {
					header = i
				}
			}
			key, _, ok := strings.Cut(trimmed, "=")
			if section && ok && strings.TrimSpace(key) == "WorkingDirectory" {
				lines[i] = "WorkingDirectory=" + SystemdWord(directory)
				found = true
			}
		}
		if header < 0 {
			return "", errors.New("native unit: missing Service section")
		}
		if !found {
			lines = append(lines[:header+1], append([]string{"WorkingDirectory=" + SystemdWord(directory)}, lines[header+1:]...)...)
		}
		return strings.Join(lines, "\n"), nil
	}
	if goos != "darwin" {
		return "", errors.New("native unit: unsupported working directory format")
	}
	if PlistArguments([]byte(body)) == nil {
		return "", errors.New("native unit: unreadable agent command")
	}
	decoder := xml.NewDecoder(strings.NewReader(body))
	depth, start, end, rootEnd := 0, -1, -1, -1
	for {
		at := int(decoder.InputOffset())
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		switch element := token.(type) {
		case xml.StartElement:
			depth++
			if depth != 3 || element.Name.Local != "key" {
				continue
			}
			var key string
			if err := decoder.DecodeElement(&key, &element); err != nil {
				return "", err
			}
			depth--
			if key != "WorkingDirectory" {
				continue
			}
			if start >= 0 {
				return "", errors.New("native unit: duplicate WorkingDirectory keys")
			}
			for {
				start = int(decoder.InputOffset())
				value, err := decoder.Token()
				if err != nil {
					return "", err
				}
				if whitespace, ok := value.(xml.CharData); ok && strings.TrimSpace(string(whitespace)) == "" {
					continue
				}
				switch value.(type) {
				case xml.Comment, xml.ProcInst:
					continue
				}
				tag, ok := value.(xml.StartElement)
				if !ok || tag.Name.Local != "string" {
					return "", errors.New("native unit: WorkingDirectory must be a string")
				}
				var entry plistElement
				if err := decoder.DecodeElement(&entry, &tag); err != nil {
					return "", err
				}
				if len(entry.Elements) > 0 {
					return "", errors.New("native unit: WorkingDirectory must be text")
				}
				end = int(decoder.InputOffset())
				break
			}
		case xml.EndElement:
			if depth == 2 && element.Name.Local == "dict" {
				rootEnd = at
			}
			depth--
		}
	}
	value := "<string>" + XMLText(directory) + "</string>"
	if start >= 0 {
		return body[:start] + value + body[end:], nil
	}
	if rootEnd < 0 {
		return "", errors.New("native unit: missing agent dictionary")
	}
	return body[:rootEnd] + fmt.Sprintf("<key>WorkingDirectory</key>%s\n", value) + body[rootEnd:], nil
}
