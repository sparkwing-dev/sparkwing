package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

type textRecord struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

func writeText(w io.Writer, kind, text, mode string) error {
	if mode == "json" {
		return json.NewEncoder(w).Encode(textRecord{Kind: kind, Text: text})
	}
	_, err := io.WriteString(w, text)
	return err
}

func requestedOutput(args []string) (string, bool, error) {
	requested, present := "", false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		switch {
		case arg == "-o" || arg == "--output":
			if i+1 == len(args) {
				return "", false, fmt.Errorf("%s requires pretty|json|plain", arg)
			}
			i++
			requested, present = args[i], true
		case strings.HasPrefix(arg, "--output="):
			requested, present = strings.TrimPrefix(arg, "--output="), true
		case strings.HasPrefix(arg, "-o="):
			requested, present = strings.TrimPrefix(arg, "-o="), true
		case strings.HasPrefix(arg, "-o") && len(arg) > 2:
			requested, present = strings.TrimPrefix(arg, "-o"), true
		}
		if present && requested == "" {
			return "", false, fmt.Errorf("--output requires pretty|json|plain")
		}
		if present {
			if _, err := resolveOutputFormat(requested, "sparkwing"); err != nil {
				return "", false, err
			}
		}
	}
	return requested, present, nil
}

func commandHelp(args []string) (*Command, bool) {
	path, selected := "sparkwing", &cmdSparkwing
	help := len(args) == 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return nil, false
		}
		if arg == "--help" || arg == "-h" || arg == "help" {
			help = true
			continue
		}
		if arg == "--output" || arg == "-o" {
			if i+1 < len(args) {
				i++
			}
			continue
		}
		if strings.HasPrefix(arg, "--output=") || strings.HasPrefix(arg, "-o=") || (strings.HasPrefix(arg, "-o") && len(arg) > 2) {
			continue
		}
		candidate, found := path+" "+arg, false
		for _, cmd := range allCommands {
			if cmd.Path == candidate {
				selected, path, found = cmd, candidate, true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return selected, help || selected == &cmdSparkwing
}

func moveRootOutput(args []string) []string {
	end := 0
	for end < len(args) {
		arg := args[end]
		if arg == "--output" || arg == "-o" {
			if end+1 >= len(args) {
				return args
			}
			end += 2
		} else if strings.HasPrefix(arg, "--output=") || strings.HasPrefix(arg, "-o=") || (strings.HasPrefix(arg, "-o") && len(arg) > 2) {
			end++
		} else {
			break
		}
	}
	if end == 0 || end == len(args) {
		return args
	}
	rest := args[end:]
	path, slot := "sparkwing", 0
	for slot < len(rest) {
		candidate, found := path+" "+rest[slot], false
		for _, cmd := range allCommands {
			if cmd.Path == candidate {
				path, found = candidate, true
				break
			}
		}
		if !found {
			break
		}
		slot++
	}
	if (path == "sparkwing run" || path == "sparkwing pipeline run") && slot < len(rest) && !strings.HasPrefix(rest[slot], "-") {
		slot++
	}
	moved := append([]string{}, rest[:slot]...)
	moved = append(moved, args[:end]...)
	return append(moved, rest[slot:]...)
}

func writeDocument(w io.Writer, slug, text, mode string) error {
	if mode != "json" {
		return writeText(w, "document", text, mode)
	}
	return json.NewEncoder(w).Encode(struct {
		Kind string `json:"kind"`
		Slug string `json:"slug,omitempty"`
		Text string `json:"text"`
	}{"document", slug, text})
}

func writeRenderedText(w io.Writer, kind, mode string, render func(io.Writer)) error {
	previous := color.Enabled()
	if mode != "pretty" {
		color.SetEnabled(false)
	}
	defer color.SetEnabled(previous)
	var text bytes.Buffer
	render(&text)
	return writeText(w, kind, text.String(), mode)
}
