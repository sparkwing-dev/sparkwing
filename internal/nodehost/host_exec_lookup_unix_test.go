//go:build unix

package nodehost

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The host searches as Node's spawn does; Go SDKs send the path they resolved.
func TestLookCommandMatchesNodeSpawn(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(dir, "bin", "swlookup")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for label, test := range map[string]struct {
		env  []string
		dir  string
		want string
	}{
		"relative entry":    {[]string{"PATH=bin:/usr/bin"}, dir, tool},
		"empty entry":       {[]string{"PATH=:/usr/bin"}, filepath.Join(dir, "bin"), tool},
		"later PATH wins":   {[]string{"PATH=/nowhere", "PATH=" + filepath.Join(dir, "bin")}, "/", tool},
		"no PATH, defaults": {[]string{}, "/", "/bin/sh"},
	} {
		name := "swlookup"
		if test.want == "/bin/sh" {
			name = "sh"
		}
		if got, err := lookCommand(name, test.env, test.dir); err != nil || got != test.want {
			t.Errorf("%s: lookCommand = %q, %v; want %q", label, got, err, test.want)
		}
	}
	if _, err := lookCommand("swlookup", []string{"PATH=/usr/bin"}, dir); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing command: %v", err)
	}
}
