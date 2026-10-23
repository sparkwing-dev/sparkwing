// Package procenv owns process environments so every placement applies the same credential boundary.
package procenv

import "github.com/sparkwing-dev/sparkwing/internal/secrets"

// Class distinguishes variable authority from the process allowed to consume it.
type Class uint8

// safety: runtime names are public values, never a transport for protocol credentials.
const (
	user Class = iota
	runtimeClass
	Protocol
	Credential
	diagnostic
	tool
)

// Kind identifies the process boundary at which Build applies policy.
type Kind uint8

// safety: hosts and services need explicit policies even when they execute no pipeline code themselves.
const (
	CLI Kind = iota
	daemonSpawn
	controller
	runner
	launcher
	runHost
	nodeHost
	jobContainer
	Plan
	Attempt
	Eval
	StepTool
	Cleanup
	Install
	HostChild
	Git
	kindCount
)

// Var is a registry row; Prefix matches a family, and engine ownership forbids inheritance.
type Var struct {
	Name       string
	Prefix     bool
	Class      Class
	Owner      string
	Doc        string
	RemoteBase bool
	Startup    bool
	GoTool     bool
}

// Base makes inherited input explicit; local children reuse Captured, never the daemon's ambient environment.
type Base struct {
	Local          bool
	OwnerMachine   bool
	Captured       []string
	Ambient        []string
	SubmitEnvAllow []string
	// Submitted names the requester's values in Ambient; pipeline processes receive them, installs do not.
	Submitted []string
	Masker    *secrets.Masker
}

// RunnerInfo carries the claim's granted identity rather than the host process's ambient identity.
type RunnerInfo struct {
	Name, Type string
	Labels     []string
}

// Runtime supplies sealed values and an attempt's already-built environment for tools and cleanups.
type Runtime struct {
	RunID, JobID                        string
	Runner                              RunnerInfo
	DryRun                              bool
	Branch, Commit, Repo, TriggerSource string
	GateIndex                           string
	Startup                             []Assignment
	AttemptEnv                          Env
}

// Assignment preserves the author's startup marker until host and SDK split job environment delivery.
type Assignment struct {
	Name, Value string
	Startup     bool
}

// JobEnv separates pre-start interpreter settings from values applied by the SDK after planning.
type JobEnv struct{ Startup, Body []Assignment }

// SplitJobEnv validates names and rejects engine-owned or credential transport names. NODE_OPTIONS,
// GODEBUG and GOMAXPROCS always belong to Startup. Startup values must not affect declarations;
// a differing attempt replan fails as plan_drift, rather than changing the accepted plan.
func SplitJobEnv(env []Assignment) (JobEnv, error) {
	if err := validate(env); err != nil {
		return JobEnv{}, err
	}
	var result JobEnv
	for _, assignment := range env {
		row, _ := Lookup(assignment.Name)
		if assignment.Startup || row.Startup {
			result.Startup = append(result.Startup, assignment)
		} else {
			result.Body = append(result.Body, assignment)
		}
	}
	return result, nil
}
