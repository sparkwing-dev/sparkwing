package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type discoveryPage struct {
	Kind       string `json:"kind"`
	Total      int    `json:"total"`
	Returned   int    `json:"returned"`
	Limit      int    `json:"limit"`
	Truncated  bool   `json:"truncated"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type discoveryPaging struct {
	limit  int
	cursor string
}

func (p discoveryPaging) bounds(keys []string) (int, int, discoveryPage, error) {
	summary := discoveryPage{Kind: "page", Total: len(keys), Limit: p.limit}
	if p.limit < 0 {
		return 0, 0, summary, fmt.Errorf("--limit must be zero or greater")
	}
	start := 0
	if p.cursor != "" {
		found := false
		for i, key := range keys {
			if key == p.cursor {
				start, found = i+1, true
				break
			}
		}
		if !found {
			return 0, 0, summary, fmt.Errorf("--cursor does not match this result set; repeat the query without a cursor")
		}
	}
	end := len(keys)
	if p.limit > 0 && p.limit < end-start {
		end = start + p.limit
	}
	summary.Returned, summary.Truncated = end-start, end < len(keys)
	if summary.Truncated {
		summary.NextCursor = keys[end-1]
	}
	return start, end, summary, nil
}

func (p discoveryPage) write(mode string) error {
	if mode == "json" {
		return json.NewEncoder(os.Stdout).Encode(p)
	}
	w := os.Stdout
	if mode == "plain" {
		w = os.Stderr
	}
	if p.Truncated {
		_, err := fmt.Fprintf(w, "%d of %d matches; continue with the same filters and --cursor %q (or --limit 0 for all)\n", p.Returned, p.Total, p.NextCursor)
		return err
	}
	if mode == "pretty" {
		_, err := fmt.Fprintf(w, "%d matches shown (%d total)\n", p.Returned, p.Total)
		return err
	}
	return nil
}

func matchesDiscoveryQuery(query, text string) bool {
	text = strings.ToLower(text)
	for _, token := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(text, token) {
			return false
		}
	}
	return true
}
