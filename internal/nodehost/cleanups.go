package nodehost

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
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

// safety: keyed by the bearer's hash, so a node reaches only its own list.
type cleanupLedger struct {
	mu    sync.Mutex
	nodes map[[bearerHashSize]byte]*cleanupList
}

// CleanupScope is where a node's cleanups run. Env is the node process's own
// environment, Dir its repository root, and Log the node's log.
type CleanupScope struct {
	Dir string
	Env []string
	Log io.Writer
}

func (s *Server) registerCleanup(w http.ResponseWriter, r *http.Request, g *grant) {
	var req struct {
		Argv        []string `json:"argv"`
		Description string   `json:"description"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCleanupBody)).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("a cleanup is a JSON object of at most %d bytes", maxCleanupBody))
		return
	}
	if msg := checkCleanup(req.Argv, req.Description); msg != "" {
		s.writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	l := &s.cleanups
	l.mu.Lock()
	defer l.mu.Unlock()
	// safety: RunCleanups requires the bearer revoked first, so a registration
	// that races it is refused here instead of landing in a list nobody runs.
	if g.ctx.Err() != nil {
		s.writeError(w, http.StatusUnauthorized, "unauthorized", "a node bearer is required")
		return
	}
	key := sha256.Sum256([]byte(g.bearer))
	if l.nodes == nil {
		l.nodes = map[[bearerHashSize]byte]*cleanupList{}
	}
	list := l.nodes[key]
	if list == nil {
		list = &cleanupList{}
		l.nodes[key] = list
	}
	if len(list.items) >= maxCleanups {
		s.writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("a node may hold at most %d cleanups; release one first", maxCleanups))
		return
	}
	list.next++
	id := strconv.Itoa(list.next)
	list.items = append(list.items, cleanup{id: id, argv: req.Argv, description: req.Description})
	s.writeJSON(w, http.StatusCreated, map[string]string{"id": id})
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

func (s *Server) releaseCleanup(w http.ResponseWriter, r *http.Request, g *grant) {
	id := r.PathValue("id")
	l := &s.cleanups
	l.mu.Lock()
	list := l.nodes[sha256.Sum256([]byte(g.bearer))]
	i := -1
	if list != nil {
		i = slices.IndexFunc(list.items, func(c cleanup) bool { return c.id == id })
	}
	if i >= 0 {
		list.items = slices.Delete(list.items, i, i+1)
	}
	l.mu.Unlock()
	if i < 0 {
		s.writeError(w, http.StatusNotFound, "unknown_cleanup", "this node holds no cleanup with that id")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RunCleanups runs every cleanup the node process holding bearer registered
// and did not release, newest first, each bounded by [CleanupTimeout]. Call it
// once the process has exited and its bearer is revoked. Each cleanup runs
// with no shell, in scope.Dir and with scope.Env, and writes its output to
// scope.Log; a failure is logged and the next cleanup still runs.
func (s *Server) RunCleanups(ctx context.Context, bearer string, scope CleanupScope) error {
	key := sha256.Sum256([]byte(bearer))
	s.mu.RLock()
	_, live := s.grants[key]
	s.mu.RUnlock()
	if live {
		return errors.New("node cleanups: revoke the node's bearer before running its cleanups")
	}
	l := &s.cleanups
	l.mu.Lock()
	list := l.nodes[key]
	delete(l.nodes, key)
	l.mu.Unlock()
	if list == nil {
		return nil
	}
	log := scope.Log
	if log == nil {
		log = io.Discard
	}
	env := nodeEnv(scope.Env)
	for _, c := range slices.Backward(list.items) {
		runCleanup(ctx, c, scope.Dir, env, log)
	}
	return nil
}

// safety: failures name argv[0] only, since later arguments may carry secrets.
func runCleanup(ctx context.Context, c cleanup, dir string, env []string, log io.Writer) {
	name := c.argv[0]
	if c.description != "" {
		fmt.Fprintf(log, "cleanup %s: %s\n", name, c.description)
	} else {
		fmt.Fprintf(log, "cleanup %s\n", name)
	}
	path, err := lookPathIn(name, env)
	if err != nil {
		fmt.Fprintf(log, "cleanup %s failed: not found in the node's PATH\n", name)
		return
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
	case errors.Is(err, procgroup.ErrCleanup):
		fmt.Fprintf(log, "cleanup %s failed: the processes it started could not be stopped\n", name)
	case errors.As(err, &exit):
		fmt.Fprintf(log, "cleanup %s failed: %s\n", name, exit.ProcessState)
	default:
		fmt.Fprintf(log, "cleanup %s failed: could not run\n", name)
	}
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
