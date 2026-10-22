package githooks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
)

type GateState string

const (
	gateUndeclared GateState = "undeclared"

	GateArmed GateState = "armed"

	GateShadowed GateState = "shadowed"

	GateUninstalled GateState = "uninstalled"

	gateBorrowed GateState = "borrowed"

	GateBroken GateState = "broken"
)

const Format = "(format 1)"

type ExpectedHook struct {
	Content  string
	Declared bool
	Forward  bool
}

type Expectations func(Git, string, string) (map[string]ExpectedHook, error)

type HookObservation struct {
	Name           string `json:"name"`
	Present        bool   `json:"present"`
	Managed        bool   `json:"managed"`
	ContentMatches bool   `json:"content_matches"`
	VersionMatches bool   `json:"version_matches"`
	Executable     bool   `json:"executable"`
	Required       bool   `json:"required"`
	Declared       bool   `json:"declared"`
	Forward        bool   `json:"forward"`
	Active         bool   `json:"active"`
	Borrowed       bool   `json:"borrowed"`
	Error          string `json:"error,omitempty"`
	Body           string `json:"-"`
}

func (h HookObservation) Current() bool {
	return h.Present && h.Managed && h.ContentMatches && h.VersionMatches && h.Executable && h.Error == ""
}

type RepoGates struct {
	Err   error             `json:"-"`
	Hooks []HookObservation `json:"hooks,omitempty"`

	Repo string `json:"repo"`

	HooksDir string `json:"hooks_dir,omitempty"`

	ActiveDir string `json:"active_dir,omitempty"`

	Scope string `json:"scope,omitempty"`

	Declared []string `json:"declared,omitempty"`

	Installed []string `json:"installed,omitempty"`

	Firing []string `json:"firing,omitempty"`

	Shadowed []string `json:"shadowed,omitempty"`

	Borrowed []string `json:"borrowed,omitempty"`

	Missing []string `json:"missing,omitempty"`

	State GateState `json:"state"`

	ConfigError string `json:"config_error,omitempty"`
}

func (r RepoGates) NotFiring() []string {
	var names []string
	for _, h := range r.Hooks {
		if !h.Borrowed && (!h.Current() || !h.Active) {
			names = append(names, h.Name)
		}
	}
	slices.Sort(names)
	return names
}

var BlockingHooks = []string{"pre-commit", "pre-push"}

// Fires names current declared hooks in the directory Git reads for this repo.
func (r RepoGates) Fires() []string {
	var out []string
	for _, h := range r.Hooks {
		if h.Declared && h.Active && h.Current() {
			out = append(out, h.Name)
		}
	}
	return out
}

func (r RepoGates) declaresBlocking() bool {
	return slices.ContainsFunc(r.Declared, func(n string) bool {
		return slices.Contains(BlockingHooks, n)
	})
}

func (r RepoGates) runsBlockingGate() bool {
	return slices.ContainsFunc(r.Fires(), func(n string) bool {
		return slices.Contains(BlockingHooks, n)
	})
}

// Gated requires a current local blocking gate and every required hook to fire.
func (r RepoGates) Gated() bool {
	if r.ConfigError != "" {
		return false
	}
	for _, h := range r.Hooks {
		if h.Required && (!h.Current() || !h.Active) {
			return false
		}
	}
	return r.runsBlockingGate()
}

func (r RepoGates) SilencedGlobal() []string {
	var names []string
	for _, h := range r.Hooks {
		if h.Forward && h.Active && !h.Current() {
			names = append(names, h.Name)
		}
	}
	return names
}

func (r RepoGates) Summary() string {
	var parts []string
	if r.ConfigError != "" {
		parts = append(parts, r.ConfigError)
	}
	for _, h := range r.Hooks {
		if h.Error != "" {
			parts = append(parts, h.Error)
		} else if h.Required && !h.Current() && !h.Borrowed {
			parts = append(parts, h.Name+" is missing or differs from expected content/format")
		}
	}
	if !r.declaresBlocking() {
		parts = append(parts, undeclaredGateSummary(r.Fires()))
	}
	if len(r.Borrowed) > 0 {
		parts = append(parts, fmt.Sprintf("%s fires out of %s, which is not this repo's hook directory, so nothing here declares or keeps it",
			strings.Join(r.Borrowed, ", "), r.ActiveDir))
	}
	if len(r.Shadowed) > 0 {
		parts = append(parts, fmt.Sprintf("%s installed but git reads %s (%s core.hooksPath), so nothing fires",
			strings.Join(r.Shadowed, ", "), r.ActiveDir, r.Scope))
	}
	if len(r.Missing) > 0 {
		parts = append(parts, fmt.Sprintf("pipelines declare %s but the hooks are missing or not current", strings.Join(r.Missing, ", ")))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%s: %s", r.Repo, r.State)
	}
	return r.Repo + ": " + strings.Join(parts, "; ")
}

func undeclaredGateSummary(fires []string) string {
	blocking := strings.Join(BlockingHooks, " or ")
	if len(fires) == 0 {
		return fmt.Sprintf("no pipeline declares %s, so nothing here refuses a commit or a push", blocking)
	}
	return fmt.Sprintf("%s fires, but no pipeline declares %s, so nothing here refuses a commit or a push",
		strings.Join(fires, ", "), blocking)
}

func (r RepoGates) Remedy() string {
	if r.Err != nil {
		return fmt.Sprintf("fix the config, then sparkwing -C %s pipeline hooks install", r.Repo)
	}
	if len(r.Borrowed) > 0 || (r.Scope == "local" && len(r.NotFiring()) > 0) {
		return fmt.Sprintf("git -C %s config --unset core.hooksPath, then sparkwing -C %s pipeline hooks install", r.Repo, r.Repo)
	}
	if r.State == GateBroken {
		return fmt.Sprintf("remove obsolete managed hooks or repair the reported hooks, then sparkwing -C %s pipeline hooks install", r.Repo)
	}
	if !r.declaresBlocking() {
		return fmt.Sprintf("declare a pre_commit trigger on a gate pipeline in %s/.sparkwing, then sparkwing -C %s pipeline hooks install", r.Repo, r.Repo)
	}
	return fmt.Sprintf("sparkwing -C %s pipeline hooks install", r.Repo)
}

func Survey(git Git, repoRoot string, expected Expectations) RepoGates {
	row := RepoGates{Repo: repoRoot, State: gateUndeclared}
	hooksDir, err := Dir(repoRoot)
	if err != nil {
		return broken(repoRoot, err)
	}
	row.HooksDir = hooksDir
	specs, err := expected(git, repoRoot, hooksDir)
	if err != nil {
		row.Err, row.ConfigError = err, err.Error()
	}
	active, scope := ActivePath(git, repoRoot)
	foreign := active != "" && !SameDir(active, hooksDir)
	var borrowed []string
	if foreign {
		row.ActiveDir, row.Scope = active, scope
		borrowed = gates(active)
	}
	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	entries, err := os.ReadDir(hooksDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return broken(repoRoot, err)
	}
	for _, e := range entries {
		if IsHookName(e.Name()) {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	failed := false
	for _, name := range slices.Compact(names) {
		spec, wanted := specs[name]
		h := HookObservation{
			Name: name, Declared: spec.Declared, Forward: spec.Forward, Active: !foreign,
			Required: spec.Forward || (spec.Declared && slices.Contains(BlockingHooks, name)),
			Borrowed: foreign && slices.Contains(borrowed, name),
		}
		path := filepath.Join(hooksDir, name)
		body, readErr := os.ReadFile(path)
		switch {
		case errors.Is(readErr, os.ErrNotExist):
		case readErr != nil:
			h.Present = true
			h.Error = fmt.Sprintf("read %s: %v", path, readErr)
		default:
			h.Present, h.Body = true, string(body)
			h.Managed = strings.Contains(h.Body, Marker)
			h.ContentMatches = wanted && h.Body == spec.Content
			h.VersionMatches = strings.Contains(h.Body, "# "+Marker+" "+Format)
			info, statErr := os.Stat(path)
			if statErr != nil {
				h.Error = statErr.Error()
			} else {
				h.Executable = info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0)
			}
		}
		if !wanted && !h.Managed && h.Error == "" {
			continue
		}
		h.Required = h.Required || (h.Managed && !wanted)
		row.Hooks = append(row.Hooks, h)
		if h.Declared {
			row.Declared = append(row.Declared, name)
		}
		if h.Managed {
			row.Installed = append(row.Installed, name)
		}
		if h.Borrowed || (h.Active && h.Current()) {
			row.Firing = append(row.Firing, name)
		}
		if h.Declared {
			switch {
			case h.Borrowed:
				row.Borrowed = append(row.Borrowed, name)
			case !h.Present || !h.Managed || !h.Executable:
				row.Missing = append(row.Missing, name)
			case !h.Active:
				row.Shadowed = append(row.Shadowed, name)
			case !h.Current():
				row.Missing = append(row.Missing, name)
			}
		}
		if h.Required && !h.Current() && !h.Borrowed {
			failed = true
		}
	}
	switch {
	case row.ConfigError != "" || failed:
		row.State = GateBroken
	case len(row.Borrowed) > 0:
		row.State = gateBorrowed
	case len(row.Missing) > 0:
		row.State = GateUninstalled
	case len(row.Shadowed) > 0:
		row.State = GateShadowed
	case len(row.Declared) > 0:
		row.State = GateArmed
	}
	return row
}

func broken(repoRoot string, err error) RepoGates {
	return RepoGates{Repo: repoRoot, State: GateBroken, ConfigError: err.Error(), Err: err}
}

func SurveyFleet(git Git, repoRoots []string, expected Expectations) []RepoGates {
	git = cacheMachineWideConfig(git)
	rows := make([]RepoGates, 0, len(repoRoots))
	for _, root := range repoRoots {
		rows = append(rows, Survey(git, root, expected))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Repo < rows[j].Repo })
	return rows
}

func Ungated(rows []RepoGates) []RepoGates {
	var out []RepoGates
	for _, r := range rows {
		if !r.Gated() {
			out = append(out, r)
		}
	}
	return out
}

func cacheMachineWideConfig(git Git) Git {
	type answer struct {
		out string
		err error
	}
	cache := map[string]answer{}
	return func(dir string, args ...string) (string, error) {
		if !slices.Contains(args, "--global") {
			return git(dir, args...)
		}
		key := strings.Join(args, "\x00")
		if a, ok := cache[key]; ok {
			return a.out, a.err
		}
		out, err := git(dir, args...)
		cache[key] = answer{out, err}
		return out, err
	}
}
