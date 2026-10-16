package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControllerHelpAndUnknownArgumentsDoNotStart(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-h", "unknown", "start"} {
		t.Run(arg, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "uncreated")
			t.Setenv("SPARKWING_HOME", home)
			args := []string{arg}
			if arg == "unknown" || arg == "start" {
				args = append(args, "--credentials-dir=/nonexistent")
			}
			err := run(args)
			help := arg == "help" || arg == "--help" || arg == "-h"
			if help && err != nil || !help && err == nil {
				t.Fatalf("argument %q: %v", arg, err)
			}
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Fatalf("startup touched home: %v", err)
			}
			if !help && err != nil && !strings.Contains(err.Error(), "unexpected argument") {
				t.Fatalf("not usage: %v", err)
			}
		})
	}
}
