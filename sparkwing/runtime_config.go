package sparkwing

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// RuntimeConfig is the snapshot of "what is true about this process
// at the moment it started." Populated once at package init from the
// environment the binary was exec'd with; stable for the lifetime of
// the run.
//
// All env reads happen in detectRuntime. Empty string is the "unset"
// sentinel.
type RuntimeConfig struct {
	// IsLocal is true on laptop-class hosts. False inside cluster
	// runner pods (detected via SPARKWING_HOST=cluster or implicit
	// KUBERNETES_SERVICE_HOST).
	IsLocal bool

	// WorkDir is the directory the pipeline should treat as the repo
	// root. Discovered at process init by walking up from cwd looking
	// for a `.sparkwing/` subdir. Empty when no project was found
	// above cwd -- helpers (Path, ReadFile, ...) then refuse to run
	// with a clear error.
	WorkDir string

	// RunID and NodeID identify the current invocation. Empty on
	// laptop dev runs (the local orchestrator assigns internally);
	// populated on cluster runs and `handle-trigger` exec'd binaries.
	RunID  string
	NodeID string

	// Git describes the source state being built. Same instance as
	// RunContext.Git. Always non-nil so live methods are safe to call
	// from init time; data fields stay empty until SetGit fills them.
	Git *Git

	// Debug reflects SPARKWING_DEBUG at process start. Mutate via
	// SetDebug; env is not re-read.
	Debug bool
}

var (
	runtimeMu sync.RWMutex
	runtime   = detectRuntime()
)

func detectRuntime() RuntimeConfig {
	rc := RuntimeConfig{
		IsLocal: detectIsLocal(),
		RunID:   os.Getenv("SPARKWING_RUN_ID"),
		NodeID:  os.Getenv("SPARKWING_NODE_ID"),
		Debug:   parseDebug(os.Getenv("SPARKWING_DEBUG")),
	}
	if cwd, err := os.Getwd(); err == nil {
		// Git-style auto-discovery: walk up from cwd looking for a
		// `.sparkwing/` subdir. Empty result means no project here;
		// helpers that need WorkDir fail loudly rather than fall back
		// to cwd.
		rc.WorkDir = walkUpToProject(cwd)
	}
	// Pre-populate Git so callers can do `runtime.Git.IsDirty(ctx)`
	// from init time without nil-checking.
	rc.Git = &Git{workDir: rc.WorkDir}
	return rc
}

// walkUpToProject ascends from start looking for a directory that
// contains a `.sparkwing/` child. Returns that directory (the repo
// root) on success, or "" on failure.
func walkUpToProject(start string) string {
	dir := start
	for {
		marker := filepath.Join(dir, ".sparkwing")
		if info, err := os.Stat(marker); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// parseDebug interprets SPARKWING_DEBUG. Empty / "0" / "false" → off;
// any other non-empty value → on.
func parseDebug(v string) bool {
	if v == "" || v == "0" || strings.EqualFold(v, "false") {
		return false
	}
	return true
}

func detectIsLocal() bool {
	if os.Getenv("SPARKWING_HOST") == "cluster" {
		return false
	}
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return false
	}
	return true
}

// CurrentRuntime returns the RuntimeConfig snapshot for this process.
// The returned value copies scalar fields; the Git pointer is shared,
// so updates via SetGit are visible to subsequent callers.
func CurrentRuntime() RuntimeConfig {
	runtimeMu.RLock()
	defer runtimeMu.RUnlock()
	return runtime
}

// Runtime is shorthand for CurrentRuntime, e.g. for
// `sparkwing.Runtime().Git.SHA`.
func Runtime() RuntimeConfig { return CurrentRuntime() }

// SetWorkDir overrides the WorkDir field. Intended for tests; also
// updates the Git workDir so live methods follow.
func SetWorkDir(dir string) {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	runtime.WorkDir = dir
	if runtime.Git == nil {
		runtime.Git = &Git{workDir: dir}
	} else {
		runtime.Git.workDir = dir
	}
}

// SetGit attaches a fully-populated Git to the runtime. Called by the
// orchestrator at run start once the trigger has been parsed. Same
// instance also lives on RunContext.Git. Later calls overwrite.
func SetGit(g *Git) {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if g == nil {
		// Keep the field non-nil so callers don't panic.
		runtime.Git = &Git{workDir: runtime.WorkDir}
		return
	}
	runtime.Git = g
}

// RunConfig is an alias for RuntimeConfig retained for compatibility.
type RunConfig = RuntimeConfig

// CurrentRunConfig is an alias for CurrentRuntime.
func CurrentRunConfig() RunConfig { return CurrentRuntime() }
