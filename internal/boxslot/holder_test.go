package boxslot_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/boxslot"
)

func TestAnnotateHolder(t *testing.T) {
	cases := []struct {
		name    string
		acquire bool
		wantErr bool
	}{
		{"appends run line to own holder", true, false},
		{"errors when this pid holds no slot", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.acquire {
				release, err := boxslot.Acquire(context.Background(), boxslot.Options{
					MaxSlots: 1,
					LockDir:  dir,
				})
				if err != nil {
					t.Fatalf("Acquire: %v", err)
				}
				defer release()
			}

			err := boxslot.AnnotateHolder(dir, "run-20260701-000000-deadbeef")

			if tc.wantErr {
				if err == nil {
					t.Fatal("AnnotateHolder without a holder: want error, got nil")
				}
				entries, readErr := os.ReadDir(dir)
				if readErr != nil {
					t.Fatalf("ReadDir: %v", readErr)
				}
				if len(entries) != 0 {
					t.Fatalf("AnnotateHolder failure left %d files behind", len(entries))
				}
				return
			}
			if err != nil {
				t.Fatalf("AnnotateHolder: %v", err)
			}

			content := readOwnHolder(t, dir)
			lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
			if len(lines) != 2 {
				t.Fatalf("holder file has %d lines, want 2:\n%s", len(lines), content)
			}
			if !strings.HasPrefix(lines[0], "pid=") || !strings.Contains(lines[0], " start=") {
				t.Errorf("line 1 = %q, want pid=<pid> start=<rfc3339>", lines[0])
			}
			if lines[1] != "run=run-20260701-000000-deadbeef" {
				t.Errorf("line 2 = %q, want run=run-20260701-000000-deadbeef", lines[1])
			}
		})
	}
}

func readOwnHolder(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "holder-") {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("read holder: %v", err)
			}
			return string(b)
		}
	}
	t.Fatal("no holder file found")
	return ""
}
