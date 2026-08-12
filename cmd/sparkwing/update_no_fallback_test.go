package main

import (
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
		updateFetchLatest = func() (string, error) { return "", errors.New("lookup denied") }
		if err := runUpdateBinary("", false, false); err == nil {
			t.Fatal("runUpdateBinary() succeeded")
		}
	})

	t.Run("release verification failure", func(t *testing.T) {
		updateDownloadInstall = func(string, string) (installedRelease, error) {
			return installedRelease{}, errors.New("signature invalid")
		}
		if err := runUpdateBinary("v0.28.0", false, false); err == nil {
			t.Fatal("runUpdateBinary() succeeded")
		}
	})
}
