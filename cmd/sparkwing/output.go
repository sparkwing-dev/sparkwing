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
