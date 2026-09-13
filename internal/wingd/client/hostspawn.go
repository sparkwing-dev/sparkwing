package client

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

const HostBinEnv = "SPARKWING_WINGD_BIN"

var ErrNoDaemonHost = errors.New("wingd/client: no admission daemon is running and no sparkwing binary is available to host one")

func ResolveHostBin() (bin string, fromEnv, ok bool) {
	return resolveHostBin(paths.UnderTest())
}

func resolveHostBin(underTest bool) (bin string, fromEnv, ok bool) {
	if bin := os.Getenv(HostBinEnv); bin != "" {
		return bin, true, true
	}
	// safety: a gate puts a source-built sparkwing on PATH, and a test that
	// found it there spawned the operator's daemon. A test that means to host a
	// daemon names the binary in $SPARKWING_WINGD_BIN; one that does not takes
	// the standalone store in its own home.
	if underTest {
		return "", false, false
	}
	if bin, err := exec.LookPath("sparkwing"); err == nil {
		return bin, false, true
	}
	return "", false, false
}

func HostSpawn() (spawn func(home, version string) error, ok bool) {
	bin, fromEnv, ok := ResolveHostBin()
	if !ok {
		return nil, false
	}
	source := "the `sparkwing` found on PATH"
	if fromEnv {
		source = "$" + HostBinEnv
	}
	return func(home, _ string) error {
		err := spawnDetached(bin, home, "")
		if err == nil {
			return nil
		}
		return fmt.Errorf("%w: start the daemon host %s (from %s): %w",
			ErrDaemonHostUnusable, bin, source, err)
	}, true
}

var ErrDaemonHostUnusable = errors.New("wingd/client: the named daemon host binary could not be started")

func NoHostSpawn(string, string) error { return ErrNoDaemonHost }
