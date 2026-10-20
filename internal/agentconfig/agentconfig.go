// Package agentconfig reads machine runner settings and private credentials.
package agentconfig

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

type Config struct {
	Contribution string `yaml:"contribution"`

	Controller     string `yaml:"controller"`
	CredentialsDir string `yaml:"credentials_dir"`
	// AllowRepos bounds use of machine-owned source credentials.
	AllowRepos []string `yaml:"repos"`
	// AcceptWorkingTree lets an agent with AllowRepos build working-tree
	// snapshots of the listed repositories. The controller supplies a
	// snapshot's code, so the list alone cannot vouch for it.
	AcceptWorkingTree bool          `yaml:"accept_working_tree"`
	Profile           string        `yaml:"profile"`
	Token             string        `yaml:"-"`
	MaxConcurrent     int           `yaml:"max_concurrent"`
	Labels            []string      `yaml:"labels"`
	SpawnPolicy       string        `yaml:"spawn_policy"`
	HolderPrefix      string        `yaml:"holder_prefix"`
	Poll              time.Duration `yaml:"poll"`
	Lease             time.Duration `yaml:"lease"`
	Heartbeat         time.Duration `yaml:"heartbeat"`

	LocalAdmission *bool `yaml:"local_admission"`

	LocalReserve string `yaml:"keep_free"`
}

// Load validates the machine file and reads runner settings and their private credential.
func Load(path string) (*Config, error) {
	var cfg Config
	found, err := userconfig.Read(path, userconfig.Runner, &cfg)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%s has no runner section; `sparkwing runners add` writes one: %w", path, os.ErrNotExist)
	}
	if cfg.CredentialsDir != "" {
		dir, err := credentials.Open(cfg.CredentialsDir)
		if err != nil {
			return nil, err
		}
		cfg.Token, err = dir.Read("agent-token")
		if err != nil {
			return nil, err
		}
		if cfg.Token == "" {
			return nil, errors.New("runner credential file is empty")
		}
	}
	return &cfg, nil
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
	if out.AcceptWorkingTree && allow.Empty() {
		return out, errors.New("agent: accept_working_tree needs allow_repos; an agent without the list builds what the controller sends")
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
	if _, err := userconfig.ParseBudget(out.LocalReserve); err != nil {
		return out, fmt.Errorf("agent: local_reserve: %w", err)
	}
	if _, err := userconfig.ParseBudget(out.Contribution); err != nil {
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
