//go:build !windows

package orchestrator

import (
	"slices"
	"testing"
)

func TestSubmissionUnixEnvironmentDoesNotAddWindowsDefaults(t *testing.T) {
	got, err := filterSubmissionEnvironment([]string{
		"USERPROFILE=/profile", "SystemRoot=/system", "ComSpec=/shell", "TEMP=/tmp", "TMP=/tmp",
		"APPDATA=/app", "LOCALAPPDATA=/local", "SPARKWING_SECRETS_KEY_FILE=/private/key",
		"PATH=/bin", "HOME=/home", "SPARKWING_CONFIG=/home/config.yaml",
	}, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=/bin", "HOME=/home", "SPARKWING_CONFIG=/home/config.yaml"}
	if !slices.Equal(got, want) {
		t.Fatalf("Unix submission defaults changed: %#v", got)
	}
}
