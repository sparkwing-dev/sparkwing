// Package agentconfig owns agent.yaml: the file a remote machine's
// sparkwing-runner reads its controller, credential and capacity ceilings
// from. It sits below both the runner that executes against the file and the
// CLI that writes one, so neither has to import the other.
//
// The file selects one of two modes. A name-less singular configuration is
// claim mode, the mode that executes work. Setting Name or Coordinators
// selects enrolled mode, which the controller refuses on both the claim route
// and the offer route; [CheckEnrolledExecutionAvailable] is the refusal every
// caller shares.
package agentconfig

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type Config struct {
	Name         string        `yaml:"name"`
	Contribution string        `yaml:"contribution"`
	Coordinators []Coordinator `yaml:"coordinators"`

	Controller    string        `yaml:"controller"`
	Logs          string        `yaml:"logs"`
	Gitcache      string        `yaml:"gitcache"`
	CacheToken    string        `yaml:"cache_token"`
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

	enrolled bool
}

// Coordinator is one explicitly enrolled controller membership.
// Each membership carries a distinct credential and may narrow the global
// slot and contribution ceilings.
type Coordinator struct {
	Name          string `yaml:"name"`
	Controller    string `yaml:"controller"`
	Logs          string `yaml:"logs"`
	Gitcache      string `yaml:"gitcache"`
	CacheToken    string `yaml:"cache_token"`
	Profile       string `yaml:"profile"`
	Token         string `yaml:"token"`
	MaxConcurrent int    `yaml:"max_concurrent"`
	Contribution  string `yaml:"contribution"`
}

// Load reads one agent.yaml. The file carries a credential, so it must be an
// owner-only regular file; an unknown field or a second YAML document is an
// error rather than something silently ignored.
func Load(path string) (*Config, error) {
	f, err := fssecure.OpenPrivateConfig(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	var cfg Config
	decoder := yaml.NewDecoder(f)
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple YAML documents are not allowed")
		}
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// Validate fills in the defaults a loaded Config leaves empty, rejects the
// settings that cannot work, and decides the mode the file selects. Callers
// run it before acting on a Config; [Config.Enrolled] reads the decision.
func Validate(in Config) (Config, error) {
	out := in
	if out.Controller == "" && len(out.Coordinators) == 0 {
		return out, errors.New("agent.yaml: controller is required")
	}
	if out.Gitcache == "" {
		out.Gitcache = strings.TrimRight(out.Controller, "/") + "/api/v1/gitcache"
	}
	if out.SpawnPolicy == "" {
		out.SpawnPolicy = "return-to-queue"
	}
	switch out.SpawnPolicy {
	case "return-to-queue":
	case "run-local", "auto":
		return out, fmt.Errorf("agent.yaml: spawn_policy %q is not implemented yet (only return-to-queue is supported in v0)", out.SpawnPolicy)
	default:
		return out, fmt.Errorf("agent.yaml: spawn_policy %q: expected return-to-queue | run-local | auto", out.SpawnPolicy)
	}
	if _, err := wingd.ParseBudget(out.LocalReserve); err != nil {
		return out, fmt.Errorf("agent.yaml: local_reserve: %w", err)
	}
	if _, err := wingd.ParseBudget(out.Contribution); err != nil {
		return out, fmt.Errorf("agent.yaml: contribution: %w", err)
	}
	out.Name = strings.TrimSpace(out.Name)
	out.enrolled = out.Name != "" || len(out.Coordinators) > 0
	if out.enrolled && strings.Contains(out.Name, ":") {
		return out, errors.New("agent.yaml: name is required and cannot contain ':'")
	}
	if out.enrolled {
		if out.LocalAdmission != nil && !*out.LocalAdmission {
			return out, errors.New("agent.yaml: local_admission cannot be false for enrolled helper memberships")
		}
		required := true
		out.LocalAdmission = &required
	} else if out.LocalAdmission == nil {
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
	membershipTokens := map[string]bool{}
	membershipControllers := map[string]bool{}
	for i := range out.Coordinators {
		member := &out.Coordinators[i]
		if member.Controller == "" {
			return out, fmt.Errorf("agent.yaml: coordinators[%d].controller is required", i)
		}
		if membershipControllers[member.Controller] {
			return out, fmt.Errorf("agent.yaml: coordinators[%d].controller is enrolled more than once", i)
		}
		membershipControllers[member.Controller] = true
		if member.Name == "" {
			member.Name = out.Name
		}
		member.Name = strings.TrimSpace(member.Name)
		if member.Name == "" || strings.Contains(member.Name, ":") {
			return out, fmt.Errorf("agent.yaml: coordinators[%d].name is required and cannot contain ':'", i)
		}
		if member.Token == "" {
			return out, fmt.Errorf("agent.yaml: coordinators[%d].token is required", i)
		}
		if membershipTokens[member.Token] {
			return out, fmt.Errorf("agent.yaml: coordinators[%d].token must be distinct", i)
		}
		membershipTokens[member.Token] = true
		if member.Gitcache == "" {
			member.Gitcache = strings.TrimRight(member.Controller, "/") + "/api/v1/gitcache"
		}
		if member.MaxConcurrent <= 0 || member.MaxConcurrent > out.MaxConcurrent {
			member.MaxConcurrent = out.MaxConcurrent
		}
		if member.Contribution == "" {
			member.Contribution = out.Contribution
		}
		if _, err := wingd.ParseBudget(member.Contribution); err != nil {
			return out, fmt.Errorf("agent.yaml: coordinators[%d].contribution: %w", i, err)
		}
	}
	if out.enrolled && len(out.Coordinators) == 0 && out.Token == "" {
		return out, errors.New("agent.yaml: token is required for an enrolled helper membership")
	}
	return out, nil
}

// EnrolledExecutionUnavailable is the whole message an agent prints when its
// configuration selects enrolled mode, which the controller refuses on both
// the claim route and the offer route.
const EnrolledExecutionUnavailable = "enrolled execution is not available; " +
	"remove name and coordinators from agent.yaml to run in claim mode, " +
	"or pass --allow-enrolled-preview to start the unfinished enrolled path"

// CheckEnrolledExecutionAvailable refuses a configuration that would enter
// enrolled mode. allowPreview lets a developer of enrolled execution run the
// unfinished path anyway.
func CheckEnrolledExecutionAvailable(cfg Config, allowPreview bool) error {
	if !cfg.enrolled || allowPreview {
		return nil
	}
	return errors.New(EnrolledExecutionUnavailable)
}

// Enrolled reports whether this configuration selects enrolled mode, which
// [Validate] decides from Name and Coordinators.
func (c Config) Enrolled() bool { return c.enrolled }

// DefaultPath is the agent.yaml a runner reads when it is given no --config.
func DefaultPath() (string, error) {
	return fssecure.ConfigFile("agent.yaml")
}
