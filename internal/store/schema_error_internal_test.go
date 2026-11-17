package store

import (
	"strings"
	"testing"
)

func TestNewerSchemaRemedyLeadsWithCLIUpgrade(t *testing.T) {
	for _, path := range []string{"", "/private/state.db"} {
		t.Run(path, func(t *testing.T) {
			message := schemaMismatchError(expectedSchemaVersion+1, path).Error()
			if !strings.HasPrefix(message, "sparkwing: upgrade the installed CLI (`sparkwing update`)") {
				t.Fatalf("refusal does not lead with an upgrade: %s", message)
			}
			warning := strings.Index(message, "backup restore loses newer runs")
			restore := strings.Index(message, "restore a backup matching this binary")
			if warning < 0 || restore < warning {
				t.Fatalf("backup advice does not warn about losing newer runs: %s", message)
			}
		})
	}
}
