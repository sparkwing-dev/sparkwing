package unitfile

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// PinEnvironment adds a missing selector while preserving an installed unit's captured value.
func PinEnvironment(goos, body, name, value string) (string, error) {
	if goos == "linux" {
		lines := strings.Split(body, "\n")
		section, end, found := false, -1, false
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[") {
				if section {
					end = i
				}
				section = trimmed == "[Service]"
			}
			if !section {
				continue
			}
			if end < 0 {
				end = len(lines)
			}
			raw, ok := strings.CutPrefix(trimmed, "Environment=")
			if !ok {
				continue
			}
			if raw == "" {
				found = false
			}
			for _, pair := range systemdWords(raw) {
				key, selected, _ := strings.Cut(pair, "=")
				if key == name {
					found = selected != ""
				}
			}
		}
		if found {
			return body, nil
		}
		if end < 0 {
			return "", errors.New("native unit: missing Service section")
		}
		lines = append(lines[:end], append([]string{"Environment=" + Assignment(name, value)}, lines[end:]...)...)
		return strings.Join(lines, "\n"), nil
	}
	if goos != "darwin" || PlistArguments([]byte(body)) == nil {
		return "", errors.New("native unit: unreadable agent environment")
	}
	decoder := xml.NewDecoder(strings.NewReader(body))
	depth, environment, atEnd := 0, false, -1
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
			if element.Name.Local != "key" {
				continue
			}
			var key string
			if err := decoder.DecodeElement(&key, &element); err != nil {
				return "", err
			}
			depth--
			if depth == 2 {
				environment = key == "EnvironmentVariables"
			}
			if environment && depth == 3 && key == name {
				return body, nil
			}
		case xml.EndElement:
			if environment && depth == 3 && element.Name.Local == "dict" {
				entry := "<key>" + XMLText(name) + "</key><string>" + XMLText(value) + "</string>\n"
				return body[:at] + entry + body[at:], nil
			}
			if depth == 2 && element.Name.Local == "dict" {
				atEnd = at
			}
			depth--
		}
	}
	if atEnd < 0 {
		return "", errors.New("native unit: missing agent dictionary")
	}
	entry := "<key>EnvironmentVariables</key><dict><key>" + XMLText(name) + "</key><string>" + XMLText(value) + "</string></dict>\n"
	return body[:atEnd] + entry + body[atEnd:], nil
}
