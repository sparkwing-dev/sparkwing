package main

import (
	"os"
	"regexp"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func TestControllerHelpFlagsMatchRegistry(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "help")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	stderr := os.Stderr
	os.Stderr = output
	defer func() { os.Stderr = stderr }()
	if err := run([]string{"--help"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	parsed := map[string]bool{}
	for _, match := range regexp.MustCompile(`(?m)^\s+--([a-z][a-z0-9-]*)(?: (string|int|duration))?\s+`).FindAllStringSubmatch(string(raw), -1) {
		parsed[match[1]] = match[2] != ""
	}
	if len(parsed) == 0 {
		t.Fatalf("no controller flags in help: %s", raw)
	}
	for _, command := range clisurface.Registry() {
		if command.Path != "sparkwing-controller" {
			continue
		}
		for _, flag := range command.Flags {
			takesArgument, exists := parsed[flag.Name]
			if !exists {
				t.Errorf("registry advertises unparsed --%s", flag.Name)
			} else if takesArgument != (flag.Argument != "") {
				t.Errorf("--%s argument differs between registry and parser", flag.Name)
			}
			delete(parsed, flag.Name)
		}
	}
	for name := range parsed {
		t.Errorf("parsed --%s is missing from registry", name)
	}
}
