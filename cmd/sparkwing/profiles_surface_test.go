package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
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
					if err := runProfilesArgs(append(args, "-o", format)); err != nil {
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
		{"show"},
		{"show", "--profile", "prod", "--show-token"},
		{"set", "--name", "prod"},
		{"remove", "--name", "prod"},
		{"add", "--controller", "https://api.example", "--no-probe", "-o", "table"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			path := profilesFixturePath(t)
			if err := runProfilesArgs(args); err == nil {
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
	err := runProfilesArgs([]string{"add", "--controller", "https://api.example", "--scope", "runs.read", "--no-probe"})
	if err == nil {
		t.Fatal("unimplemented credential enrollment silently succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed enrollment wrote config: %v", err)
	}
}

func TestProfilesRejectArgvTokenWithoutWriting(t *testing.T) {
	path := profilesFixturePath(t)
	if err := writeConnectionFixture(path, "prod", "https://api.example", "old-credential"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	output := captureStdout(t, func() {
		err = runProfilesArgs([]string{"set", "--profile=prod", "--token=private-credential"})
	})
	var usage *clisurface.UsageError
	if !errors.As(err, &usage) || exitCodeFor(err) != 2 || output != "" || strings.Contains(err.Error(), "private-credential") {
		t.Fatalf("output=%q err=%v", output, err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(before, after) {
		t.Fatal("removed flag changed credentials")
	}
}

func FuzzProfilesRejectTokenValuesBeforeSideEffects(f *testing.F) {
	f.Add("credential")
	f.Add("\n引数\"value")
	f.Fuzz(func(t *testing.T, value string) {
		value = "private-" + value
		for _, args := range [][]string{
			{"profiles", "set", "--profile=prod", "--token=" + value},
			{"profiles", "add", "--controller=https://api.example", "--scope=" + value},
		} {
			_, err := clisurface.Parse("sparkwing", args)
			var usage *clisurface.UsageError
			if !errors.As(err, &usage) || strings.Contains(err.Error(), value) {
				t.Fatalf("removed credential surface accepted or disclosed: %v", err)
			}
		}
	})
}
