package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDoctorGoToolchain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake go uses a POSIX shell")
	}
	for _, tc := range []struct{ name, setting, floor, overlay, verdict, override string }{
		{"sufficient", "local", "1.26.6", "", "ok", ""},
		{"fixed pin", "go1.26.6", "1.26.8", "", "sparkwing will build with go1.26.8", "go1.26.8"},
		{"overlay floor", "go1.26.6", "1.26", "1.26.8", "sparkwing will build with go1.26.8", "go1.26.8"},
		{"blocked", "local", "1.26.8", "", "blocked by GOTOOLCHAIN=local", ""},
		{"automatic", "auto", "1.26.8", "", "ok", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/pipeline\ngo "+tc.floor+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.overlay != "" {
				if err := os.WriteFile(filepath.Join(dir, ".resolved.mod"), []byte("module example.com/pipeline\ngo "+tc.overlay+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			fake := filepath.Join(dir, "go")
			if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\ngo1.26.6\\n/path/to/go/env\\n' \"$GOTOOLCHAIN\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			got := inspectDoctorGoToolchain(context.Background(), dir, []string{"PATH=" + dir, "GOTOOLCHAIN=" + tc.setting})
			if got.Current != "go1.26.6" || got.Setting != tc.setting || got.Source != "the GOTOOLCHAIN environment variable" || got.Verdict != tc.verdict || got.Override != tc.override {
				t.Fatalf("finding = %+v", got)
			}
			wantFloor := tc.floor
			if tc.overlay != "" {
				wantFloor = tc.overlay
			}
			if got.Floor != wantFloor {
				t.Fatalf("floor = %q, want %q", got.Floor, wantFloor)
			}
			if tc.name == "blocked" {
				for _, want := range []string{"1.26.8", "1.26.6", "GOTOOLCHAIN=local", "the GOTOOLCHAIN environment variable", "unset GOTOOLCHAIN"} {
					if !strings.Contains(got.Error, want) {
						t.Errorf("error %q missing %q", got.Error, want)
					}
				}
			} else if got.Error != "" {
				t.Fatalf("error = %s", got.Error)
			}
		})
	}
}
