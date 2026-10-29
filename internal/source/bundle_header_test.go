package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundlePrerequisitesAcceptBothGitObjectFormats(t *testing.T) {
	for _, format := range []struct {
		version, capability string
		length              int
	}{{"v2", "", 40}, {"v3", "@object-format=sha256\n", 64}} {
		t.Run(format.version, func(t *testing.T) {
			sha := strings.Repeat("a", format.length)
			path := filepath.Join(t.TempDir(), "source.bundle")
			header := "# " + format.version + " git bundle\n" + format.capability + "-" + sha + " prerequisite\n\n"
			if err := os.WriteFile(path, []byte(header), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := bundlePrerequisites(path)
			if err != nil || len(got) != 1 || got[0] != sha {
				t.Fatalf("prerequisites=%v: %v", got, err)
			}
		})
	}
}
