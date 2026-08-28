//go:build !windows

package logs

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPrivateRootCreatesPrivateLogTree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "logs-service")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	server, err := NewPrivate(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	client := NewClient(httpServer.URL, nil)
	if err := client.Append(context.Background(), "run-1", "build", []byte("private\n")); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		filepath.Join(root, "runs"):                       0o700,
		filepath.Join(root, "runs", "run-1"):              0o700,
		filepath.Join(root, "runs", "run-1", "build.log"): 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %04o, want %04o", path, got, want)
		}
	}
}

func TestSharedRootPreservesPVCCompatibleLogModes(t *testing.T) {
	if os.Getenv("SPARKWING_LOGS_UMASK_HELPER") != "1" {
		root := filepath.Join(t.TempDir(), "logs-service")
		cmd := exec.Command(os.Args[0], "-test.run=^TestSharedRootPreservesPVCCompatibleLogModes$")
		cmd.Env = append(os.Environ(),
			"SPARKWING_LOGS_UMASK_HELPER=1",
			"SPARKWING_LOGS_TEST_ROOT="+root,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("umask helper: %v\n%s", err, out)
		}
		return
	}

	syscall.Umask(0o027)
	root := os.Getenv("SPARKWING_LOGS_TEST_ROOT")
	server, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(root, ".health-check")
	if err := server.writeFile(canary, []byte("ok")); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	client := NewClient(httpServer.URL, nil)
	if err := client.Append(context.Background(), "run-1", "build", []byte("shared\n")); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		root:                                 0o750,
		canary:                               0o640,
		filepath.Join(root, "runs"):          0o750,
		filepath.Join(root, "runs", "run-1"): 0o750,
		filepath.Join(root, "runs", "run-1", "build.log"): 0o640,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %04o, want %04o", path, got, want)
		}
	}
}
