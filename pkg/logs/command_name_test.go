package logs

import (
	"os"
	"strings"
	"testing"
)

func TestPackageCommentsUsePublicRunsLogsName(t *testing.T) {
	for _, name := range []string{"client.go", "server.go"} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(body), "sparkwing jobs logs") {
			t.Errorf("%s names the retired sparkwing jobs logs command", name)
		}
	}
}
