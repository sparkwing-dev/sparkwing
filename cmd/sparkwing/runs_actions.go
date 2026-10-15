package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

func collectRunIDs(flagIDs []string, stdin io.Reader) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		id := normalizeRunID(raw)
		if seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	var sawDash bool
	for _, id := range flagIDs {
		if id == "-" {
			sawDash = true
			continue
		}
		add(id)
	}
	if sawDash {
		if stdin == nil {
			return nil, errors.New("--run - requested but no stdin available")
		}
		sc := bufio.NewScanner(stdin)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			add(sc.Text())
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
	}
	return out, nil
}

func profileSuffix(on string) string {
	if on == "" {
		return ""
	}
	return " --profile " + on
}
