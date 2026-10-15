//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssertReleaseVersionReportsWhatTheChildPrinted(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "version-fixture")
	rejecting := []byte("#!/bin/sh\necho \"unknown flag: --offline\" >&2\nexit 2\n")
	if err := os.WriteFile(bin, rejecting, 0o700); err != nil {
		t.Fatal(err)
	}
	err := assertReleaseVersion(bin, "v9.9.9")
	if err == nil {
		t.Fatal("a release that rejects the version query was accepted")
	}
	for _, want := range []string{"version -o json --offline", "unknown flag: --offline", "exit status 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err.Error(), want)
		}
	}
}
