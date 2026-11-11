package bincache

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestUnreadableCacheInputNamesReadAccessRepair(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("fixture requires Unix file permission enforcement")
	}
	dir := newPipelineDir(t)
	writeFile(t, filepath.Join(dir, "main.go"), "package main\nimport _ \"embed\"\n//go:embed marker.txt\nvar marker string\nfunc main(){}\n")
	input := filepath.Join(dir, "marker.txt")
	if err := os.WriteFile(input, []byte("fixture"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(input, 0o600) })
	_, _, err := ExplainCacheKey(dir, os.Environ())
	var refusal *apierr.Error
	if !errors.Is(err, fs.ErrPermission) || !errors.As(err, &refusal) || refusal.Code != apierr.BadRequest || !strings.Contains(refusal.Message, "marker.txt") || !strings.Contains(refusal.Hint, "read access") {
		t.Fatalf("unreadable input lost its configuration repair: %v", err)
	}
	if err := os.Chmod(input, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ExplainCacheKey(dir, os.Environ()); err != nil {
		t.Fatalf("restoring owner read access did not repair the input: %v", err)
	}
}
