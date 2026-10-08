//go:build windows

package wingd

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsSocketHomeAliasesReachTheSameElectedListener(t *testing.T) {
	home := filepath.Clean(t.TempDir())
	forward := filepath.ToSlash(home)
	t.Chdir(filepath.Dir(home))
	relative := filepath.Base(home)
	aliases := []string{home, forward, strings.ToLower(home[:1]) + home[1:], strings.ToLower(home), strings.ToUpper(home), home + `\unused\..`, relative}
	daemon, err := New(Config{Home: forward, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	elected, err := daemon.elect()
	if err != nil || !elected {
		t.Fatalf("election = %v, %v", elected, err)
	}
	defer daemon.releaseLock()
	listener, err := daemon.bindListener()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.RemoveAll(filepath.Dir(daemon.layout.sock))
	}()
	for _, alias := range aliases {
		t.Run(alias, func(t *testing.T) {
			layout, err := resolveLayout(alias)
			if err != nil || !strings.EqualFold(layout.home, daemon.layout.home) || layout.sock != daemon.layout.sock {
				t.Fatalf("alias layout differs from elected layout: %+v, %v", layout, err)
			}
			held, err := LockHeld(alias)
			if err != nil || !held {
				t.Fatalf("alias election held = %v, %v", held, err)
			}
			connection, err := net.DialTimeout("unix", layout.sock, 3*time.Second)
			if err != nil {
				t.Fatalf("alias cannot reach elected listener: %v", err)
			}
			if err := connection.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

const socketBaseChildEnv = "SPARKWING_TEST_SOCKET_BASE_OUT"

func TestWindowsSocketBaseDirIgnoresTheEnvironment(t *testing.T) {
	if out := os.Getenv(socketBaseChildEnv); out != "" {
		if err := os.WriteFile(out, []byte(testSocketBase(t)), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	base := testSocketBase(t)
	// hack: shell32 caches known folders per process, so only a fresh process
	// resolves the base under a moved environment.
	out := filepath.Join(t.TempDir(), "base")
	child := exec.Command(os.Args[0], "-test.run=^TestWindowsSocketBaseDirIgnoresTheEnvironment$")
	child.Env = append(os.Environ(), socketBaseChildEnv+"="+out,
		"TEMP="+t.TempDir(), "TMP="+t.TempDir(), "USERPROFILE="+t.TempDir())
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("socket base child with TEMP, TMP and USERPROFILE moved: %v\n%s", err, output)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != base {
		t.Fatalf("socketBaseDir() = %q with TEMP, TMP and USERPROFILE moved, want the fixed base %q", got, base)
	}
	home := t.TempDir()
	sock, err := SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	_, identity, err := socketHomeIdentity(home)
	if err != nil {
		t.Fatal(err)
	}
	if want := socketPathIn(base, identity); sock != want {
		t.Fatalf("SocketPath = %q, want the derived path %q", sock, want)
	}
}
