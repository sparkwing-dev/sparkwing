// Package backends loads backends.yaml -- the file that declares
// where the three persistence surfaces live: cache (content-addressed
// artifacts and compiled pipeline binaries), logs (per-job log
// streams), and state (run records, plan snapshots, status).
//
// Source precedence (per-field, repo wins):
//
//  1. .sparkwing/backends.yaml          -- team-shared, checked in
//  2. ~/.config/sparkwing/backends.yaml -- per-user additions / overrides
//
// A name in both files merges per non-zero field with repo values
// winning.
//
// Selection at process start:
//
//  1. Per-target overlay (pipelines.yaml targets.<name>.backend)
//  2. Auto-detected environment (first matching environments.<name>.detect)
//  3. File.Defaults block
//
// Shape (yaml):
//
//	defaults:
//	  cache:
//	    type: filesystem
//	    path: ~/.cache/sparkwing
//	  logs:
//	    type: filesystem
//	    path: ~/.cache/sparkwing/logs
//	  state:
//	    type: sqlite
//	    path: ~/.cache/sparkwing/state.db
//
//	environments:
//	  gha:
//	    detect: { env_var: GITHUB_ACTIONS, equals: "true" }
//	    cache: { type: s3, bucket: sparkwing-cache, prefix: ${GITHUB_REPOSITORY}/ }
//	    logs:  { type: s3, bucket: sparkwing-logs,  prefix: ${GITHUB_REPOSITORY}/ }
//	  kubernetes:
//	    detect: { env_var: KUBERNETES_SERVICE_HOST, present: true }
//	    cache: { type: controller }
//	    logs:  { type: controller }
package backends

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// File is the on-disk shape of backends.yaml.
type File struct {
	Defaults     Surfaces               `yaml:"defaults,omitempty"`
	Environments map[string]Environment `yaml:"environments,omitempty"`

	// envOrder preserves declaration order for environment
	// auto-detection (first match wins). yaml.v3 maps are unordered,
	// so Load walks the decoded yaml.Node to capture it.
	envOrder []string `yaml:"-"`
}

// Environment is one entry under environments:. Name is populated
// from the map key during Load.
type Environment struct {
	Name     string `yaml:"-"`
	Detect   Detect `yaml:"detect,omitempty"`
	Surfaces `yaml:",inline"`
}

// Detect describes when an Environment matches.
//
// EnvVar names the variable to consult. Equals matches a specific
// value; Present matches any non-empty value. Setting neither is
// invalid (validated in Validate).
type Detect struct {
	EnvVar  string `yaml:"env_var,omitempty"`
	Equals  string `yaml:"equals,omitempty"`
	Present bool   `yaml:"present,omitempty"`
}

// Surfaces groups the three persistence destinations. A nil pointer
// means "not overridden at this layer."
type Surfaces struct {
	Cache *Spec `yaml:"cache,omitempty"`
	Logs  *Spec `yaml:"logs,omitempty"`
	State *Spec `yaml:"state,omitempty"`
}

// Spec is one backend declaration. Type is the discriminator; the
// remaining fields are interpreted per-type and validated in
// Validate.
type Spec struct {
	Type string `yaml:"type"`

	Bucket    string `yaml:"bucket,omitempty"`
	Prefix    string `yaml:"prefix,omitempty"`
	Path      string `yaml:"path,omitempty"`
	URL       string `yaml:"url,omitempty"`
	URLSource string `yaml:"url_source,omitempty"`
	Token     string `yaml:"token,omitempty"`

	// Binaries is an optional nested override on Cache that isolates
	// compiled pipeline binaries to a separate destination (e.g.
	// shared s3 bucket while the rest of cache stays on disk). Only
	// valid on the cache surface.
	Binaries *Spec `yaml:"binaries,omitempty"`
}

// Surface identifies one of the three persistence destinations.
type Surface string

const (
	SurfaceCache Surface = "cache"
	SurfaceLogs  Surface = "logs"
	SurfaceState Surface = "state"
)

// Backend type discriminators.
const (
	TypeFilesystem = "filesystem"
	TypeS3         = "s3"
	TypeGCS        = "gcs"
	TypeAzureBlob  = "azure-blob"
	TypeController = "controller"
	TypeStdout     = "stdout"
	TypeSQLite     = "sqlite"
	TypePostgres   = "postgres"
	TypeMySQL      = "mysql"
)

// allowedTypes is the per-surface allow-list. Mirrors the design
// doc's "Backend types" table.
var allowedTypes = map[Surface]map[string]bool{
	SurfaceCache: {
		TypeFilesystem: true,
		TypeS3:         true,
		TypeGCS:        true,
		TypeAzureBlob:  true,
		TypeController: true,
	},
	SurfaceLogs: {
		TypeFilesystem: true,
		TypeS3:         true,
		TypeGCS:        true,
		TypeAzureBlob:  true,
		TypeController: true,
		TypeStdout:     true,
	},
	SurfaceState: {
		TypeSQLite:     true,
		TypePostgres:   true,
		TypeMySQL:      true,
		TypeController: true,
	},
}

// Load reads a single backends.yaml file. A missing file is NOT an
// error -- it returns an empty File. Parse errors, unknown keys, and
// validation failures bubble up so the path appears in the message.
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
		if errors.Is(err, io.EOF) {
			return File{}, nil
		}
		return File{}, fmt.Errorf("parse %s: %w", path, err)
	}
	for name, e := range f.Environments {
		e.Name = name
		f.Environments[name] = e
	}
	// Recover declaration order so environment auto-detect is
	// deterministic across processes.
	f.envOrder = readEnvironmentsOrder(raw)
	if err := f.Validate(); err != nil {
		return File{}, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// readEnvironmentsOrder parses the same yaml a second time at the
// node level to recover declaration order of the environments: map.
// Maps come back unordered from Decode, but the underlying node tree
// preserves source order.
func readEnvironmentsOrder(raw []byte) []string {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "environments" {
			continue
		}
		envMap := root.Content[i+1]
		if envMap.Kind != yaml.MappingNode {
			return nil
		}
		out := make([]string, 0, len(envMap.Content)/2)
		for j := 0; j+1 < len(envMap.Content); j += 2 {
			out = append(out, envMap.Content[j].Value)
		}
		return out
	}
	return nil
}

// EnvironmentOrder returns the environments in declaration order.
// Synthesized entries appended after Load (e.g. built-in detect
// rules) sort alphabetically at the tail so behavior is deterministic.
func (f *File) EnvironmentOrder() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(f.Environments))
	for _, name := range f.envOrder {
		if _, ok := f.Environments[name]; ok && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	rest := make([]string, 0, len(f.Environments))
	for name := range f.Environments {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// Validate checks structural invariants across the file.
func (f *File) Validate() error {
	if err := validateSurfaces(f.Defaults, "defaults"); err != nil {
		return err
	}
	for name, env := range f.Environments {
		if env.Detect.EnvVar == "" {
			return fmt.Errorf("environment %q: detect.env_var is required", name)
		}
		if env.Detect.Equals == "" && !env.Detect.Present {
			return fmt.Errorf("environment %q: detect requires either equals or present: true", name)
		}
		if err := validateSurfaces(env.Surfaces, "environments."+name); err != nil {
			return err
		}
	}
	return nil
}

func validateSurfaces(s Surfaces, where string) error {
	if s.Cache != nil {
		if err := validateSpec(*s.Cache, SurfaceCache, where+".cache"); err != nil {
			return err
		}
	}
	if s.Logs != nil {
		if err := validateSpec(*s.Logs, SurfaceLogs, where+".logs"); err != nil {
			return err
		}
	}
	if s.State != nil {
		if err := validateSpec(*s.State, SurfaceState, where+".state"); err != nil {
			return err
		}
	}
	return nil
}

func validateSpec(spec Spec, surface Surface, where string) error {
	if spec.Type == "" {
		return fmt.Errorf("%s: type is required", where)
	}
	allowed, ok := allowedTypes[surface]
	if !ok {
		return fmt.Errorf("%s: unknown surface %q", where, surface)
	}
	if !allowed[spec.Type] {
		return fmt.Errorf("%s: type %q not allowed on %s surface (valid: %s)",
			where, spec.Type, surface, listAllowed(surface))
	}
	switch spec.Type {
	case TypeS3, TypeGCS, TypeAzureBlob:
		if spec.Bucket == "" {
			return fmt.Errorf("%s: type=%s requires bucket", where, spec.Type)
		}
	case TypeFilesystem:
		if spec.Path == "" {
			return fmt.Errorf("%s: type=%s requires path", where, spec.Type)
		}
	case TypePostgres, TypeMySQL:
		if (spec.URL == "") == (spec.URLSource == "") {
			return fmt.Errorf("%s: type=%s requires exactly one of url or url_source", where, spec.Type)
		}
	}
	if spec.Binaries != nil {
		if surface != SurfaceCache {
			return fmt.Errorf("%s: binaries override is only valid on cache surface", where)
		}
		if err := validateSpec(*spec.Binaries, SurfaceCache, where+".binaries"); err != nil {
			return err
		}
		if spec.Binaries.Binaries != nil {
			return fmt.Errorf("%s.binaries: nested binaries override is not allowed", where)
		}
	}
	return nil
}

func listAllowed(surface Surface) string {
	types := make([]string, 0, len(allowedTypes[surface]))
	for t := range allowedTypes[surface] {
		types = append(types, t)
	}
	sort.Strings(types)
	return strings.Join(types, ", ")
}

// UserConfigPath returns the per-user backends.yaml location.
// Honors $XDG_CONFIG_HOME.
func UserConfigPath() (string, error) {
	if env := os.Getenv("XDG_CONFIG_HOME"); env != "" {
		return filepath.Join(env, "sparkwing", "backends.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".config", "sparkwing", "backends.yaml"), nil
}

// RepoConfigPath is .sparkwing/backends.yaml inside the repo.
func RepoConfigPath(sparkwingDir string) string {
	return filepath.Join(sparkwingDir, "backends.yaml")
}

// Resolve loads both files and merges them. Repo values win per
// non-zero field; user values fill blanks.
func Resolve(sparkwingDir string) (File, error) {
	user, err := loadUser()
	if err != nil {
		return File{}, err
	}
	var repo File
	if sparkwingDir != "" {
		repo, err = Load(RepoConfigPath(sparkwingDir))
		if err != nil {
			return File{}, err
		}
	}
	return Merge(repo, user), nil
}

func loadUser() (File, error) {
	path, err := UserConfigPath()
	if err != nil {
		return File{}, err
	}
	return Load(path)
}

// Merge combines repo and user files. Repo values win per non-zero
// field; user values fill blanks. Environment declaration order is
// preserved: repo entries first, then user-only entries.
func Merge(repo, user File) File {
	out := File{
		Defaults: mergeSurfaces(repo.Defaults, user.Defaults),
	}
	if len(repo.Environments)+len(user.Environments) == 0 {
		return out
	}
	out.Environments = map[string]Environment{}
	for name, env := range repo.Environments {
		if u, ok := user.Environments[name]; ok {
			out.Environments[name] = mergeEnvironment(env, u)
		} else {
			out.Environments[name] = env
		}
	}
	for name, env := range user.Environments {
		if _, ok := repo.Environments[name]; !ok {
			out.Environments[name] = env
		}
	}
	seen := map[string]bool{}
	for _, name := range repo.envOrder {
		if _, ok := out.Environments[name]; ok && !seen[name] {
			seen[name] = true
			out.envOrder = append(out.envOrder, name)
		}
	}
	for _, name := range user.envOrder {
		if _, ok := out.Environments[name]; ok && !seen[name] {
			seen[name] = true
			out.envOrder = append(out.envOrder, name)
		}
	}
	return out
}

func mergeEnvironment(repo, user Environment) Environment {
	merged := repo
	if merged.Detect.EnvVar == "" {
		merged.Detect.EnvVar = user.Detect.EnvVar
	}
	if merged.Detect.Equals == "" {
		merged.Detect.Equals = user.Detect.Equals
	}
	if !merged.Detect.Present {
		merged.Detect.Present = user.Detect.Present
	}
	merged.Surfaces = mergeSurfaces(repo.Surfaces, user.Surfaces)
	return merged
}

func mergeSurfaces(repo, user Surfaces) Surfaces {
	return Surfaces{
		Cache: mergeSpec(repo.Cache, user.Cache),
		Logs:  mergeSpec(repo.Logs, user.Logs),
		State: mergeSpec(repo.State, user.State),
	}
}

func mergeSpec(repo, user *Spec) *Spec {
	if repo == nil {
		return user
	}
	if user == nil {
		return repo
	}
	merged := *repo
	if merged.Type == "" {
		merged.Type = user.Type
	}
	if merged.Bucket == "" {
		merged.Bucket = user.Bucket
	}
	if merged.Prefix == "" {
		merged.Prefix = user.Prefix
	}
	if merged.Path == "" {
		merged.Path = user.Path
	}
	if merged.URL == "" {
		merged.URL = user.URL
	}
	if merged.URLSource == "" {
		merged.URLSource = user.URLSource
	}
	if merged.Token == "" {
		merged.Token = user.Token
	}
	merged.Binaries = mergeSpec(merged.Binaries, user.Binaries)
	return &merged
}

// DetectEnvironment walks Environments in declaration order and
// returns the first whose Detect rule evaluates true against
// os.Getenv. Returns ok=false when none match.
func DetectEnvironment(f File) (string, Environment, bool) {
	for _, name := range f.EnvironmentOrder() {
		env := f.Environments[name]
		if matchesDetect(env.Detect) {
			return name, env, true
		}
	}
	return "", Environment{}, false
}

func matchesDetect(d Detect) bool {
	if d.EnvVar == "" {
		return false
	}
	v, ok := os.LookupEnv(d.EnvVar)
	if !ok {
		return false
	}
	if d.Equals != "" {
		return v == d.Equals
	}
	if d.Present {
		return v != ""
	}
	return false
}

// Effective resolves the surfaces that apply to a run. Precedence
// (per-field, first non-nil wins): target overlay > environment >
// defaults.
func Effective(f File, envName string, target Surfaces) Surfaces {
	out := f.Defaults
	if envName != "" {
		if env, ok := f.Environments[envName]; ok {
			out = layerSurfaces(out, env.Surfaces)
		}
	}
	return layerSurfaces(out, target)
}

func layerSurfaces(base, over Surfaces) Surfaces {
	return Surfaces{
		Cache: layerSpec(base.Cache, over.Cache),
		Logs:  layerSpec(base.Logs, over.Logs),
		State: layerSpec(base.State, over.State),
	}
}

// layerSpec overlays over on top of base per non-zero field. A
// different over.Type takes everything from over and ignores base
// (a kind change resets the spec).
func layerSpec(base, over *Spec) *Spec {
	if over == nil {
		return base
	}
	if base == nil || base.Type != over.Type {
		clone := *over
		return &clone
	}
	merged := *over
	if merged.Bucket == "" {
		merged.Bucket = base.Bucket
	}
	if merged.Prefix == "" {
		merged.Prefix = base.Prefix
	}
	if merged.Path == "" {
		merged.Path = base.Path
	}
	if merged.URL == "" {
		merged.URL = base.URL
	}
	if merged.URLSource == "" {
		merged.URLSource = base.URLSource
	}
	if merged.Token == "" {
		merged.Token = base.Token
	}
	if merged.Binaries == nil {
		merged.Binaries = base.Binaries
	}
	return &merged
}
