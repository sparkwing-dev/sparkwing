package main

import (
	"errors"
	"testing"
)

func TestUpdateDoesNotExecuteUnverifiedFallback(t *testing.T) {
	originalFetch := updateFetchLatest
	originalDownload := updateDownloadInstall
	originalFallback := updateGoInstallFallback
	originalVersion := Version
	t.Cleanup(func() {
		updateFetchLatest = originalFetch
		updateDownloadInstall = originalDownload
		updateGoInstallFallback = originalFallback
		Version = originalVersion
	})
	Version = "v0.27.0"

	t.Run("latest lookup failure", func(t *testing.T) {
		fallbackCalls := 0
		updateFetchLatest = func() (string, error) { return "", errors.New("lookup denied") }
		updateGoInstallFallback = func(string) error {
			fallbackCalls++
			return nil
		}
		if err := runUpdateBinary("", false, false); err == nil {
			t.Fatal("runUpdateBinary() succeeded")
		}
		if fallbackCalls != 0 {
			t.Fatalf("fallback calls = %d, want 0", fallbackCalls)
		}
	})

	t.Run("release verification failure", func(t *testing.T) {
		fallbackCalls := 0
		updateDownloadInstall = func(string, string) error { return errors.New("signature invalid") }
		updateGoInstallFallback = func(string) error {
			fallbackCalls++
			return nil
		}
		if err := runUpdateBinary("v0.28.0", false, false); err == nil {
			t.Fatal("runUpdateBinary() succeeded")
		}
		if fallbackCalls != 0 {
			t.Fatalf("fallback calls = %d, want 0", fallbackCalls)
		}
	})
}
