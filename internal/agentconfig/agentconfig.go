// Package agentconfig owns the agent section of config.yaml: the settings a
// remote machine's sparkwing-runner reads its controller, credential and
// capacity ceilings from. It sits below both the runner that executes against
// the section and the CLI that writes one, so neither has to import the other.
//
// The section describes claim mode, the mode that executes work: the runner
// polls the controller's claim route and runs what it is awarded. A section
// that still carries the removed enrolled-mode keys fails to load with
// [EnrolledModeRemoved].
package agentconfig

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type Config struct {
	Contribution string `yaml:"contribution"`

	Controller string `yaml:"controller"`
	Logs       string `yaml:"logs"`
	// Gitcache is the git cache source is fetched through. Empty means the
	// controller's gitcache proxy, unless AllowRepos is set.
	Gitcache string `yaml:"gitcache"`
	// AllowRepos names the repositories this machine may build, as host/path
	// patterns. Given without a gitcache, the agent fetches each run's source
	// directly: with the credential the controller releases, else with the
	// machine owner's own git credentials.
	AllowRepos    []string      `yaml:"allow_repos"`
	Profile       string        `yaml:"profile"`
	Token         string        `yaml:"token"`
	MaxConcurrent int           `yaml:"max_concurrent"`
	Labels        []string      `yaml:"labels"`
	SpawnPolicy   string        `yaml:"spawn_policy"`
	HolderPrefix  string        `yaml:"holder_prefix"`
	Poll          time.Duration `yaml:"poll"`
	Lease         time.Duration `yaml:"lease"`
	Heartbeat     time.Duration `yaml:"heartbeat"`

	LocalAdmission *bool `yaml:"local_admission"`

	LocalReserve string `yaml:"local_reserve"`
}

// EnrolledModeRemoved is the whole message a configuration carrying the
// removed enrolled-mode keys fails to load with.
const EnrolledModeRemoved = "enrolled mode has been removed; " +
	"delete name and coordinators from the agent section of config.yaml to run in claim mode, which executes work"

// Load reads the agent section of the config.yaml at path. The file carries a
// credential, so it must be an owner-only regular file; an unknown field is an
// error rather than something silently ignored. A file without the section
// fails with an error that wraps [os.ErrNotExist].
func Load(path string) (*Config, error) {
	// safety: the unknown-field error names the key without naming the mode it
	// used to select, so the removed keys are answered before the ordinary parse.
	section, err := userconfig.Node(path, userconfig.Agent)
	if err != nil {
		if cfg, legacy, lerr := loadLegacyFile(path); legacy {
			return cfg, lerr
		}
		return nil, err
	}
	if carriesEnrolledKey(section, 0) {
		return nil, fmt.Errorf("parse %s: %s", path, EnrolledModeRemoved)
	}
	var cfg Config
	found, err := userconfig.Read(path, userconfig.Agent, &cfg)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%s has no agent section; `sparkwing cluster runners add` writes one: %w", path, os.ErrNotExist)
	}
	return &cfg, nil
}

// safety: a merge key hides the removed keys behind an alias and YAML admits a
// key in any case, so both reach the removal message instead of an
// unknown-field error that names neither the mode nor the key that selected it.
func carriesEnrolledKey(node *yaml.Node, depth int) bool {
	const maxMergeDepth = 8
	if node == nil || depth > maxMergeDepth {
		return false
	}
	switch node.Kind {
	case yaml.AliasNode:
		return carriesEnrolledKey(node.Alias, depth+1)
	case yaml.SequenceNode:
		for _, item := range node.Content {
			if carriesEnrolledKey(item, depth+1) {
				return true
			}
		}
		return false
	case yaml.MappingNode:
	default:
		return false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		switch strings.ToLower(strings.TrimSpace(node.Content[i].Value)) {
		case "name", "coordinators":
			return true
		case "<<":
			if carriesEnrolledKey(node.Content[i+1], depth+1) {
				return true
			}
		}
	}
	return false
}

// Validate fills in the defaults a loaded Config leaves empty and rejects the
// settings that cannot work. Callers run it before acting on a Config.
func Validate(in Config) (Config, error) {
	out := in
	if out.Controller == "" {
		return out, errors.New("agent: controller is required")
	}
	allow, err := sourceurl.ParseRepoAllowlist(out.AllowRepos)
	if err != nil {
		return out, fmt.Errorf("agent: allow_repos: %w", err)
	}
	if !allow.Empty() {
		out.AllowRepos = allow.Patterns()
	}
	// safety: an agent with no list keeps the proxy, since without one it
	// would fetch whatever a run names with its owner's credentials.
	if out.Gitcache == "" && allow.Empty() {
		out.Gitcache = strings.TrimRight(out.Controller, "/") + "/api/v1/gitcache"
	}
	if out.SpawnPolicy == "" {
		out.SpawnPolicy = "return-to-queue"
	}
	switch out.SpawnPolicy {
	case "return-to-queue":
	case "run-local", "auto":
		return out, fmt.Errorf("agent: spawn_policy %q is not implemented yet (only return-to-queue is supported in v0)", out.SpawnPolicy)
	default:
		return out, fmt.Errorf("agent: spawn_policy %q: expected return-to-queue | run-local | auto", out.SpawnPolicy)
	}
	if _, err := wingd.ParseBudget(out.LocalReserve); err != nil {
		return out, fmt.Errorf("agent: local_reserve: %w", err)
	}
	if _, err := wingd.ParseBudget(out.Contribution); err != nil {
		return out, fmt.Errorf("agent: contribution: %w", err)
	}
	if out.LocalAdmission == nil {
		disabled := false
		out.LocalAdmission = &disabled
	}
	if out.MaxConcurrent < 1 {
		out.MaxConcurrent = 1
	}
	if out.Poll <= 0 {
		out.Poll = 500 * time.Millisecond
	}
	if out.Lease <= 0 {
		out.Lease = store.DefaultLeaseDuration
	}
	clean := make([]string, 0, len(out.Labels))
	seen := map[string]bool{}
	for _, l := range out.Labels {
		l = strings.TrimSpace(l)
		if l != "" && !seen[l] {
			seen[l] = true
			clean = append(clean, l)
		}
	}
	out.Labels = clean
	return out, nil
}

// DefaultPath is the config.yaml a runner reads when it is given no --config.
func DefaultPath() (string, error) {
	return userconfig.Path()
}
