// Package procenv owns process environments so every placement applies the same credential boundary.
package procenv

import "github.com/sparkwing-dev/sparkwing/internal/secrets"

// Class distinguishes variable authority from the process allowed to consume it.
type Class uint8

// safety: runtime names are public values, never a transport for protocol credentials.
const (
	User Class = iota
	RuntimeClass
	Protocol
	Credential
	Diagnostic
	Tool
)

// Kind identifies the process boundary at which Build applies policy.
type Kind uint8

// safety: hosts and services need explicit policies even when they execute no pipeline code themselves.
const (
	CLI Kind = iota
	DaemonSpawn
	Controller
	Runner
	Launcher
	RunHost
	NodeHost
	JobContainer
	Plan
	Attempt
	Eval
	StepTool
	Cleanup
	Install
	HostChild
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
}

// Policy declares every class's delivery rule for a process kind; credentials use files or memory only.
type Policy struct {
	Kind                                                  Kind
	User, Runtime, Protocol, Credential, Diagnostic, Tool string
}

// Policies returns the complete process policy, including service and subprocess boundaries.
func Policies() []Policy {
	return []Policy{
		{CLI, "documented CLI names", "none", "toolchain marker and test markers", "own key input only", "debug and output format", "ambient tool settings"},
		{DaemonSpawn, "arguments", "none", "arguments", "key file", "output format argument", "minimal and XDG"},
		{Controller, "flags", "none", "arguments", "credentials directory", "flags", "AWS, OTEL, KUBECONFIG, proxies and CA"},
		{Runner, "flags", "none", "arguments", "credentials directory", "flags", "AWS, OTEL, KUBECONFIG, proxies and CA"},
		{Launcher, "flags", "none", "arguments", "credentials directory", "flags", "AWS, OTEL, KUBECONFIG, proxies and CA"},
		{RunHost, "none", "compose from sealed values", "arguments and frames", "memory", "none", "none"},
		{NodeHost, "none", "compose from sealed values", "arguments and frames", "memory", "none", "none"},
		{JobContainer, "none", "none", "arguments and bootstrap file", "file exchanged once", "none", "HOME, caches, proxies and CA"},
		{Plan, "local captured or remote allow list", "run, runner, dry run, source and hook", "none", "none", "debug", "base"},
		{Attempt, "local captured or remote allow list", "plan plus job", "none", "none", "debug", "base plus startup; body after plan"},
		{Eval, "local captured or remote allow list", "plan plus job", "none", "none", "debug", "base plus startup; body after plan"},
		{StepTool, "inherit attempt", "inherit attempt", "none", "none", "inherit attempt", "inherit attempt including body"},
		{Cleanup, "inherit attempt", "inherit attempt", "none", "none", "inherit attempt", "inherit attempt including body"},
		{Install, "base only", "none", "none", "none", "none", "base; output masked"},
		{HostChild, "captured snapshot", "none", "arguments", "none", "none", "captured snapshot; host binary via argument"},
	}
}

// Base makes inherited input explicit; local children reuse Captured, never the daemon's ambient environment.
type Base struct {
	Local          bool
	Captured       []string
	Ambient        []string
	SubmitEnvAllow []string
	Masker         *secrets.Masker
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
	GateIndex                           *int
	Startup                             []Assignment
	AttemptEnv                          []string
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
func SplitJobEnv(env []Assignment) (JobEnv, error) { panic("unimplemented: c4-surface-config") }

// Build excludes credential-class values from both local snapshots and remote inputs under Policies.
// This includes tool-owned credentials in a local snapshot. Plan never receives job Startup;
// Attempt and Eval receive it before process start. The SDK adds Body only after plan succeeds.
// Cleanup and StepTool use AttemptEnv exactly. Remote allow-list values of at least twelve characters
// are masked before any subprocess starts. Build drops envredact.CredentialName matches before
// applying allow lists, even when Lookup classifies an unknown name as user-owned.
func Build(k Kind, b Base, rt Runtime) ([]string, error) { panic("unimplemented: c4-surface-config") }

// Lookup uses exact rows before prefixes, case-insensitively on Windows; unknown names remain user-owned.
func Lookup(name string) (Var, bool) { panic("unimplemented: c4-surface-config") }

// Validate refuses invalid names and engine-owned assignments without exposing secret values in its errors.
func Validate(env []Assignment) error { panic("unimplemented: c4-surface-config") }
