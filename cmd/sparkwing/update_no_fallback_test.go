package main

import (
	"context"
	"errors"
	"testing"
)

func TestUpdateDoesNotExecuteUnverifiedFallback(t *testing.T) {
	originalFetch := updateFetchLatest
	originalDownload := updateDownloadInstall
	originalVersion := Version
	t.Cleanup(func() {
		updateFetchLatest = originalFetch
		updateDownloadInstall = originalDownload
		Version = originalVersion
	})
	Version = "v0.27.0"

	t.Run("latest lookup failure", func(t *testing.T) {
		updateFetchLatest = func(context.Context) (string, error) { return "", errors.New("lookup denied") }
		if _, err := updateBinary("", false, false); err == nil {
			t.Fatal("updateBinary() succeeded")
		}
	})

	t.Run("release verification failure", func(t *testing.T) {
		updateDownloadInstall = func(string, string) (installedRelease, error) {
			return installedRelease{}, errors.New("signature invalid")
		}
		if _, err := updateBinary("v0.28.0", false, false); err == nil {
			t.Fatal("updateBinary() succeeded")
		}
	})
}
