package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationGoTestIsolatesParentButKeepsBackends(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/integration-probe\n\ngo 1.26.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := `package probe
import (
	"os"
	"strings"
	"testing"
	"time"
)
func TestEnvironment(t *testing.T) {
	for _, name := range strings.Split(os.Getenv("QA_FORBIDDEN"), ",") {
		if _, present := os.LookupEnv(name); present { t.Errorf("inherited %s", name) }
	}
	for name, want := range map[string]string{
		"SPARKWING_TEST_PG_URL": "postgres://fixture",
		"AWS_ENDPOINT_URL_S3": "http://fixture",
		"SPARKWING_REQUIRE_PG": "1",
		"AWS_REGION": "us-east-1",
	} {
		if got := os.Getenv(name); got != want { t.Errorf("%s = %q, want %q", name, got, want) }
	}
	if os.Getenv("GOMAXPROCS") == "" { t.Error("go test has no CPU bound") }
	if got := os.Getenv("SPARKWING_HOME"); got == "" || got == "parent-home" {
		t.Errorf("test home = %q, want its own home", got)
	}
	if deadline, ok := t.Deadline(); !ok || time.Until(deadline) < 23*time.Minute {
		t.Errorf("test deadline = %v, want the shared 25m timeout", deadline)
	}
}
`
	if err := os.WriteFile(filepath.Join(root, "probe_test.go"), []byte(probe), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append([]string{}, os.Environ()...)
	for _, name := range productTestUnset {
		env = append(env, name+"=parent")
	}
	env = append(env,
		"QA_FORBIDDEN="+strings.Join(productTestUnset, ","),
		"SPARKWING_HOME=parent-home",
		"SPARKWING_TEST_PG_URL=postgres://fixture",
		"AWS_ENDPOINT_URL_S3=http://fixture",
		"SPARKWING_REQUIRE_PG=1",
		"AWS_REGION=us-east-1",
	)
	out, err := goTest(context.Background(), root, env, []string{"-run", "^TestEnvironment$", "-count=1", "./..."})
	if err != nil {
		t.Fatalf("integration test child inherited its parent: %v\n%s", err, out)
	}
}
