package gitcontrol_test

import (
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/testsupport/gitcontrol"
)

func TestPublicUnbindPreservesConfiguration(t *testing.T) {
	t.Setenv("GIT_INDEX_FILE", "fixture-index")
	t.Setenv("GIT_CONFIG_GLOBAL", "fixture-config")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	if err := gitcontrol.Unbind(); err != nil {
		t.Fatal(err)
	}
	if _, exists := os.LookupEnv("GIT_INDEX_FILE"); exists {
		t.Fatal("binding retained")
	}
	if os.Getenv("GIT_CONFIG_GLOBAL") != "fixture-config" || os.Getenv("GIT_TERMINAL_PROMPT") != "0" {
		t.Fatal("configuration removed")
	}
}
