//go:build unix

package nodehost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func TestProcessCleanupsReportFailuresAndContinueInReverseOrder(t *testing.T) {
	cleanups := &ProcessCleanups{}
	for _, argv := range [][]string{
		{"/bin/sh", "-c", `printf oldest >> order`},
		{"/bin/sh", "-c", `exit 3`, "private-argument"},
		{"/missing-program", "private-argument"},
		{"/bin/sh", "-c", `printf newest >> order`},
	} {
		if _, err := cleanups.Register(argv, ""); err != nil {
			t.Fatal(err)
		}
	}
	scope := CleanupScope{Dir: t.TempDir(), Env: procenv.Env{}}
	if err := cleanups.Run(t.Context(), scope); err == nil || strings.Contains(err.Error(), "private-argument") {
		t.Fatalf("cleanup failure became success or exposed arguments: %v", err)
	}
	written, err := os.ReadFile(filepath.Join(scope.Dir, "order"))
	if err != nil || string(written) != "newestoldest" {
		t.Fatalf("cleanup order=%q error=%v", written, err)
	}
	if err := cleanups.Run(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(filepath.Join(scope.Dir, "order"))
	if err != nil || string(again) != string(written) {
		t.Fatal("cleanup commands ran more than once")
	}
}
