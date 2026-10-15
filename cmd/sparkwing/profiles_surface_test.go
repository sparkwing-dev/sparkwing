package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestProfilesSurfaceMutationOutputs(t *testing.T) {
	for _, format := range []string{"pretty", "json", "plain"} {
		t.Run(format, func(t *testing.T) {
			path := profilesFixturePath(t)
			for _, args := range [][]string{
				{"add", "--controller", "https://api.example", "--profile", "prod", "--no-probe"},
				{"set", "--profile", "prod", "--controller", "https://other.example"},
				{"show", "--profile", "prod"},
				{"list"},
				{"remove", "--profile", "prod", "--keep-token"},
			} {
				out := captureStdout(t, func() {
					if err := runProfiles(append(args, "-o", format)); err != nil {
						t.Fatal(err)
					}
				})
				if !strings.Contains(out, "prod") {
					t.Fatalf("missing result: %q", out)
				}
				if format == "plain" && out != "prod\n" {
					t.Fatalf("plain result: %q", out)
				}
				if format == "json" {
					var record map[string]any
					if err := json.Unmarshal([]byte(out), &record); err != nil {
						t.Fatalf("non-record output: %q (%v)", out, err)
					}
					if record["name"] != "prod" {
						t.Fatalf("result: %#v", record)
					}
				}
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProfilesSurfaceRejectsOldFlagsBeforeWriting(t *testing.T) {
	for _, args := range [][]string{
		{"add", "--controller", "https://api.example", "--name", "prod", "--no-probe"},
		{"add", "--controller", "https://api.example", "--admin-token-stdin", "--no-probe"},
		{"show"}, {"show", "--profile", "prod", "--show-token"},
		{"set", "--name", "prod"}, {"remove", "--name", "prod"},
		{"add", "--controller", "https://api.example", "--no-probe", "-o", "table"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			path := profilesFixturePath(t)
			if err := runProfiles(args); err == nil {
				t.Fatal("accepted removed or invalid surface")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("invalid command wrote config: %v", err)
			}
		})
	}
}

func TestProfilesSurfaceScopeCannotSilentlySucceed(t *testing.T) {
	path := profilesFixturePath(t)
	err := runProfiles([]string{"add", "--controller", "https://api.example", "--scope", "runs.read", "--no-probe"})
	if err == nil {
		t.Fatal("unimplemented credential enrollment silently succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed enrollment wrote config: %v", err)
	}
}
