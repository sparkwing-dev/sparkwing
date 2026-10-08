//go:build windows

package wingd

import (
	"net"
	"os"
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

func TestWindowsSocketBaseDirIgnoresTheEnvironment(t *testing.T) {
	base := testSocketBase(t)
	for _, name := range []string{"TEMP", "TMP", "USERPROFILE"} {
		t.Setenv(name, t.TempDir())
		if got := testSocketBase(t); got != base {
			t.Fatalf("socketBaseDir() = %q with %s moved, want the fixed base %q", got, name, base)
		}
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
