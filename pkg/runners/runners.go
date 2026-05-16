// Package runners loads runners.yaml -- the file that names the
// runners a pipeline can dispatch jobs to. Each entry declares the
// labels it advertises and, for cluster-backed types, the spec used
// to materialize a runner pod. Job-level selection (Job.Requires /
// Prefers / WhenRunner) matches against these advertised labels.
//
// Source precedence (per-field, repo wins):
//
//  1. .sparkwing/runners.yaml         -- team-shared, checked in
//  2. ~/.config/sparkwing/runners.yaml -- per-user additions / overrides
//
// A name present in both files is merged with repo values winning
// per non-zero field; user-only fields fill blanks. Names only in
// the user file resolve as-is.
//
// Implicit local: if neither file declares a runner named "local",
// Resolve("local") and Names synthesize one carrying labels for the
// current host's OS and architecture. A user-declared "local" entry
// overrides the synthesized version.
//
// Shape (yaml):
//
//	runners:
//	  local:
//	    type: local
//	    labels: [local, "os=darwin"]
//	  cloud-linux:
//	    type: kubernetes
//	    controller: shared
//	    labels: [cloud-linux, "os=linux"]
//	    spec:
//	      nodeSelector: { karpenter.sh/nodepool: general }
//	      resources:
//	        requests: { cpu: 2, memory: 4Gi }
//	  mac-mini:
//	    type: static
//	    labels: [mac-mini, "os=macos"]
package runners

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"go.yaml.in/yaml/v3"
)

// File is the on-disk shape of runners.yaml.
type File struct {
	Runners map[string]Runner `yaml:"runners"`
}

// Runner is one named entry under runners:. Name is populated from
// the map key during Load; it is not read off the wire.
type Runner struct {
	Name string `yaml:"-"`

	// Type is the runner kind. Valid values:
	//   "local"      -- in-process, on whichever host runs the CLI or controller
	//   "kubernetes" -- pod materialized by a Kubernetes runner pool
	//   "static"     -- long-lived runner that registers itself
	Type string `yaml:"type"`

	// Controller is the named profile from profiles.yaml that hosts
	// this runner. Required for type=="kubernetes"; ignored for
	// "local" and "static".
	Controller string `yaml:"controller,omitempty"`

	// Labels are the equality strings the runner advertises. Empty
	// strings inside the slice are silently dropped, matching how
	// Job.Requires filters its argument list.
	Labels []string `yaml:"labels,omitempty"`

	// Spec carries Kubernetes-only materialization details. Setting
	// it on a non-kubernetes entry is a validation error.
	Spec Spec `yaml:"spec,omitempty"`
}

// Spec is the kubernetes-only spec block: pod placement and
// resources. Other runner types ignore it.
type Spec struct {
	NodeSelector map[string]string `yaml:"nodeSelector,omitempty"`
	Tolerations  []Toleration      `yaml:"tolerations,omitempty"`
	Resources    Resources         `yaml:"resources,omitempty"`
}

// Toleration mirrors corev1.Toleration. The orchestrator stamps these
// onto the runner pod spec at dispatch.
type Toleration struct {
	Key      string `yaml:"key,omitempty"`
	Operator string `yaml:"operator,omitempty"`
	Value    string `yaml:"value,omitempty"`
	Effect   string `yaml:"effect,omitempty"`
}

// Resources mirrors corev1.ResourceRequirements with string values
// (so "2", "2Gi", "1000m" round-trip without quantity parsing here).
type Resources struct {
	Requests map[string]string `yaml:"requests,omitempty"`
	Limits   map[string]string `yaml:"limits,omitempty"`
}

// Load reads a single runners.yaml file. A missing file is NOT an
// error -- it returns an empty File. Parse errors, unknown keys,
// and validation failures bubble up so operators see the file path
// in the message instead of silent fallback to defaults.
func Load(path string) (File, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return File{}, fmt.Errorf("read %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil {
		return File{}, fmt.Errorf("parse %s: %w", path, err)
	}
	for name, r := range f.Runners {
		r.Name = name
		r.Labels = filterEmpty(r.Labels)
		f.Runners[name] = r
	}
	if err := f.Validate(); err != nil {
		return File{}, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// Validate checks every entry's structural invariants: Type is one
// of the documented values, Kubernetes runners declare a Controller,
// and non-kubernetes runners do not carry a Spec block.
func (f *File) Validate() error {
	for name, r := range f.Runners {
		switch r.Type {
		case "local", "kubernetes", "static":
			// ok
		case "":
			return fmt.Errorf("runner %q: type is required (one of: local, kubernetes, static)", name)
		default:
			return fmt.Errorf("runner %q: unknown type %q (valid: local, kubernetes, static)", name, r.Type)
		}
		if r.Type == "kubernetes" && r.Controller == "" {
			return fmt.Errorf("runner %q: type=kubernetes requires a controller field", name)
		}
		if r.Type != "kubernetes" && !specIsZero(r.Spec) {
			return fmt.Errorf("runner %q: spec block is only valid for type=kubernetes", name)
		}
	}
	return nil
}

func specIsZero(s Spec) bool {
	return len(s.NodeSelector) == 0 &&
		len(s.Tolerations) == 0 &&
		len(s.Resources.Requests) == 0 &&
		len(s.Resources.Limits) == 0
}

func filterEmpty(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// UserConfigPath returns the per-user runners.yaml location. Honors
// $XDG_CONFIG_HOME so it sits alongside the wing config under the
// same parent directory.
func UserConfigPath() (string, error) {
	if env := os.Getenv("XDG_CONFIG_HOME"); env != "" {
		return filepath.Join(env, "sparkwing", "runners.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".config", "sparkwing", "runners.yaml"), nil
}

// RepoConfigPath is .sparkwing/runners.yaml inside the repo.
// sparkwingDir is the discovered repo-level config directory.
func RepoConfigPath(sparkwingDir string) string {
	return filepath.Join(sparkwingDir, "runners.yaml")
}

// Resolve loads both files, merges them, and returns the named
// runner. Repo values win per non-zero field; user values fill
// blanks. Returns (_, true, nil) on success; (_, false, nil) when
// no file declares the name (and the name isn't the synthesized
// implicit "local"); (_, false, err) on parse, validation, or IO
// failures.
func Resolve(sparkwingDir, name string) (Runner, bool, error) {
	repoFile, userFile, err := loadBoth(sparkwingDir)
	if err != nil {
		return Runner{}, false, err
	}

	repo, inRepo := repoFile.Runners[name]
	user, inUser := userFile.Runners[name]

	if !inRepo && !inUser {
		if name == "local" {
			return implicitLocal(), true, nil
		}
		return Runner{}, false, nil
	}

	if inRepo && !inUser {
		repo.Name = name
		return repo, true, nil
	}
	if !inRepo && inUser {
		user.Name = name
		return user, true, nil
	}

	// Both declared: repo wins per non-zero field; user fills blanks.
	merged := repo
	merged.Name = name
	if merged.Type == "" {
		merged.Type = user.Type
	}
	if merged.Controller == "" {
		merged.Controller = user.Controller
	}
	if len(merged.Labels) == 0 {
		merged.Labels = user.Labels
	}
	if specIsZero(merged.Spec) {
		merged.Spec = user.Spec
	}
	return merged, true, nil
}

// Names returns every runner name visible across both files, with
// "local" prepended when neither file declares it. Repo entries
// appear before user-only additions; iteration order within a file
// follows Go's map iteration and is therefore not stable -- callers
// that need a deterministic order should sort.
func Names(sparkwingDir string) ([]string, error) {
	repoFile, userFile, err := loadBoth(sparkwingDir)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var out []string
	if _, repoHas := repoFile.Runners["local"]; !repoHas {
		if _, userHas := userFile.Runners["local"]; !userHas {
			out = append(out, "local")
			seen["local"] = true
		}
	}
	for n := range repoFile.Runners {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for n := range userFile.Runners {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

func loadBoth(sparkwingDir string) (repo, user File, err error) {
	userPath, err := UserConfigPath()
	if err != nil {
		return File{}, File{}, err
	}
	user, err = Load(userPath)
	if err != nil {
		return File{}, File{}, err
	}
	if sparkwingDir != "" {
		repo, err = Load(RepoConfigPath(sparkwingDir))
		if err != nil {
			return File{}, File{}, err
		}
	}
	return repo, user, nil
}

// implicitLocal is the synthesized "local" runner returned when no
// file declares it. The advertised labels carry the host's OS and
// architecture so jobs gated by os=darwin / arch=arm64 line up with
// the CLI's actual machine.
func implicitLocal() Runner {
	return Runner{
		Name: "local",
		Type: "local",
		Labels: []string{
			"local",
			"os=" + runtime.GOOS,
			"arch=" + runtime.GOARCH,
		},
	}
}
