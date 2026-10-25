package clisurface

import (
	"fmt"
	"strings"
)

// validate catches grammar drift before a parser or help generator consumes it.
func validate(commands []Command) error {
	paths := make(map[string]bool, len(commands))
	for _, c := range commands {
		if c.Path == "" || strings.TrimSpace(c.Path) != c.Path || paths[c.Path] {
			return fmt.Errorf("invalid or duplicate command %q", c.Path)
		}
		paths[c.Path] = true
	}
	for _, c := range commands {
		if i := strings.LastIndexByte(c.Path, ' '); i >= 0 && !paths[c.Path[:i]] {
			return fmt.Errorf("%s: missing parent", c.Path)
		}
		pipeline := c.Path == "sparkwing run" || c.Path == "sparkwing pipeline trigger"
		if c.PipelineArgument != pipeline || c.PipelineArgsAfterSeparator != (pipeline || c.Path == "sparkwing pipeline plan") {
			return fmt.Errorf("%s: invalid positional grammar", c.Path)
		}
		names, shorts := map[string]bool{}, map[string]bool{}
		flags := append(GlobalFlags(), c.Flags...)
		for _, f := range flags {
			if f.Name == "" || names[f.Name] {
				return fmt.Errorf("%s: duplicate or empty flag %q", c.Path, f.Name)
			}
			names[f.Name] = true
			if f.Short != "" {
				expected := map[string]string{"o": "output", "q": "quiet", "f": "follow", "v": "sw-verbose", "C": "checkout", "h": "help"}
				if expected[f.Short] != f.Name || shorts[f.Short] {
					return fmt.Errorf("%s: invalid short flag %q", c.Path, f.Short)
				}
				shorts[f.Short] = true
			}
			if f.Repeatable != f.CommaList || (f.Repeatable && f.Argument == "") {
				return fmt.Errorf("%s: invalid repetition for --%s", c.Path, f.Name)
			}
			if f.Name == "limit" && (!f.HasMinimum || f.Minimum != 1) {
				return fmt.Errorf("%s: invalid page limit", c.Path)
			}
			if f.Name == "name" || f.Name == "id" || f.Name == "prefix" || f.Name == "node" {
				return fmt.Errorf("%s: non-kind selector --%s", c.Path, f.Name)
			}
			if f.Name == "grep" && c.Path != "sparkwing runs grep" && c.Path != "sparkwing runs logs" {
				return fmt.Errorf("%s: grep outside logs", c.Path)
			}
		}
		for _, f := range flags {
			for _, n := range append(append([]string{}, f.Requires...), f.Conflicts...) {
				if !names[n] || n == f.Name {
					return fmt.Errorf("%s: invalid flag relation --%s -> --%s", c.Path, f.Name, n)
				}
			}
		}
		if names["limit"] && !names["cursor"] && c.Path != "sparkwing daemon events" {
			return fmt.Errorf("%s: limit needs cursor", c.Path)
		}
	}
	return nil
}
