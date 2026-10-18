package main

import (
	"bytes"
	"encoding/json"
	"io"

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
