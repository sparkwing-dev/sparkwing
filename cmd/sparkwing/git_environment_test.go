package main

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func TestNativeGitEnvironmentExcludesInheritedControlsProperty(t *testing.T) {
	property := func(value uint64) bool {
		controls := []string{"GIT_NO_REPLACE_OBJECTS", "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_CONFIG_COUNT", "GIT_CONFIG_GLOBAL", "GIT_SSH_COMMAND", "GIT_ASKPASS", "SSH_ASKPASS", "SSH_AUTH_SOCK", "SPARKWING_TOKEN"}
		captured := []string{"PATH=/bin", "HOME=/private/home", "GIT_SSL_CAINFO=/ca.pem", "GIT_SSL_CAPATH=/certs"}
		for _, name := range controls {
			captured = append(captured, name+"="+strings.Repeat("x", int(value%64)+1))
		}
		got, err := procenv.Build(procenv.Git, procenv.Base{Local: true, Captured: captured}, procenv.Runtime{})
		want := []string{"GIT_NO_REPLACE_OBJECTS=1", "GIT_SSL_CAINFO=/ca.pem", "GIT_SSL_CAPATH=/certs", "HOME=/private/home", "PATH=/bin"}
		if err != nil || !reflect.DeepEqual(slices.Collect(got.Values()), want) {
			t.Logf("Git environment = %v, error = %v", got, err)
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}
