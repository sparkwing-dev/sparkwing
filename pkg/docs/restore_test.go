package docs_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func TestRestoreGuideExplainsOrphanCloudRecovery(t *testing.T) {
	guide, err := docs.Read("backup-restore")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"orphan", "sparkwing.dev/quarantined", "taint and annotation", "uncordon"} {
		if !strings.Contains(guide, step) {
			t.Errorf("restore guide omits Cloud recovery step %q", step)
		}
	}
}
