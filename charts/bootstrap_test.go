package charts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestDocumentedBootstrapCommandsProvisionController(t *testing.T) {
	for _, path := range []string{"sparkwing-full/values.yaml", "sparkwing-full/README.md"} {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			command := regexp.MustCompile(`printf '[^\n` + "`" + `]+`).FindString(string(data))
			command, _, _ = strings.Cut(command, " > ")
			if command == "" {
				t.Fatal("bootstrap credential generator is missing")
			}
			raw, err := exec.Command("sh", "-c", command).Output()
			if err != nil {
				t.Fatalf("bootstrap generator failed: %v", err)
			}
			st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := st.Close(); err != nil {
					t.Error(err)
				}
			})
			created, err := controller.EnsureBootstrapAdminToken(t.Context(), st, string(raw))
			if err != nil || !created {
				t.Fatalf("documented credential refused by controller: created=%v err=%v", created, err)
			}
			if ready, err := st.HasDeploymentAdministrator(t.Context()); err != nil || !ready {
				t.Fatalf("documented bootstrap did not provision administrator: ready=%v err=%v", ready, err)
			}
		})
	}
}
