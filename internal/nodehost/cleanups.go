package nodehost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

// CleanupTimeout bounds each cleanup a node registered. It is one constant for
// every node, not a per-node setting.
const CleanupTimeout = 2 * time.Minute

// safety: these bound what one node process may register, so a node cannot
// grow the host's memory or its exit work without bound.
const (
	maxCleanups           = 16
	maxCleanupArgs        = 64
	maxCleanupBody        = 16 << 10
	maxCleanupDescription = 256
	// safety: a killed cleanup's leftover children may hold its output open only this long.
	cleanupWaitDelay = 5 * time.Second
)

type cleanup struct {
	id          string
	argv        []string
	description string
}

type cleanupList struct {
	next  int
	items []cleanup
}

// CleanupScope is where a node's cleanups run. Env is the node process's own
// environment, Dir its repository root, and Log the node's log.
type CleanupScope struct {
	Dir string
	Env []string
	Log io.Writer
}

func checkCleanup(argv []string, description string) string {
	switch {
	case len(argv) == 0 || argv[0] == "":
		return "argv must name a program"
	case len(argv) > maxCleanupArgs:
		return fmt.Sprintf("argv may hold at most %d elements", maxCleanupArgs)
	case len(description) > maxCleanupDescription:
		return fmt.Sprintf("description may be at most %d bytes", maxCleanupDescription)
	case slices.ContainsFunc(argv, func(a string) bool { return strings.ContainsRune(a, 0) }):
		return "argv may not contain NUL bytes"
	}
	return ""
}

// safety: failures name argv[0] only, since later arguments may carry secrets.
func runCleanup(ctx context.Context, c cleanup, dir string, env []string, log io.Writer) error {
	name := c.argv[0]
	if c.description != "" {
		fmt.Fprintf(log, "cleanup %s: %s\n", name, c.description)
	} else {
		fmt.Fprintf(log, "cleanup %s\n", name)
	}
	path, err := lookPathIn(name, env)
	if err != nil {
		fmt.Fprintf(log, "cleanup %s failed: not found in the node's PATH\n", name)
		return errors.New("registered cleanup program is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, CleanupTimeout)
	defer cancel()
	cmd := exec.Command(path, c.argv[1:]...)
	cmd.Args[0] = name
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdout, cmd.Stderr = log, log
	cmd.WaitDelay = cleanupWaitDelay
	err = runCleanupProcess(ctx, cmd)
	var exit *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		fmt.Fprintf(log, "cleanup %s failed: timed out\n", name)
	case ctx.Err() != nil:
		fmt.Fprintf(log, "cleanup %s failed: cancelled\n", name)
	case err == nil || errors.Is(err, exec.ErrWaitDelay):
		return nil
	case errors.Is(err, procgroup.ErrCleanup):
		fmt.Fprintf(log, "cleanup %s failed: the processes it started could not be stopped\n", name)
	case errors.As(err, &exit):
		fmt.Fprintf(log, "cleanup %s failed: %s\n", name, exit.ProcessState)
	default:
		fmt.Fprintf(log, "cleanup %s failed: could not run\n", name)
	}
	return errors.New("registered cleanup command failed")
}

// safety: never nil, since a nil Env hands a cleanup the host's environment.
func nodeEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "SPARKWING_NODE_TOKEN=") {
			out = append(out, kv)
		}
	}
	return out
}

// safety: the node's PATH picks the program, not the host's; relative entries
// are skipped so the host's working directory never decides it.
func lookPathIn(name string, env []string) (string, error) {
	if strings.ContainsAny(name, `/\`) {
		return name, nil
	}
	var path string
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if k == "PATH" || (runtime.GOOS == "windows" && strings.EqualFold(k, "PATH")) {
			path = v
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		if p, err := exec.LookPath(filepath.Join(dir, name)); err == nil {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}
