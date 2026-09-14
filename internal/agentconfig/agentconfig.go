// Package agentconfig owns agent.yaml: the file a remote machine's
// sparkwing-runner reads its controller, credential and capacity ceilings
// from. It sits below both the runner that executes against the file and the
// CLI that writes one, so neither has to import the other.
//
// The file describes claim mode, the mode that executes work: the runner polls
// the controller's claim route and runs what it is awarded. A file that still
// carries the removed enrolled-mode keys fails to load with
// [EnrolledModeRemoved].
package agentconfig

import (
	"bytes"
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
	Contribution string `yaml:"contribution"`

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
}

// EnrolledModeRemoved is the whole message a configuration carrying the
// removed enrolled-mode keys fails to load with.
const EnrolledModeRemoved = "enrolled mode has been removed; " +
	"delete name and coordinators from agent.yaml to run in claim mode, which executes work"

// Load reads one agent.yaml. The file carries a credential, so it must be an
// owner-only regular file; an unknown field or a second YAML document is an
// error rather than something silently ignored.
func Load(path string) (*Config, error) {
	f, err := fssecure.OpenPrivateConfig(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if selectsEnrolledMode(data) {
		return nil, fmt.Errorf("parse %s: %s", path, EnrolledModeRemoved)
	}
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
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

// safety: the unknown-field error names the key without naming the mode it
// used to select, so the removed keys are answered before the ordinary parse.
func selectsEnrolledMode(data []byte) bool {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
		return false
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		switch root.Content[i].Value {
		case "name", "coordinators":
			return true
		}
	}
	return false
}

// Validate fills in the defaults a loaded Config leaves empty and rejects the
// settings that cannot work. Callers run it before acting on a Config.
func Validate(in Config) (Config, error) {
	out := in
	if out.Controller == "" {
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

// DefaultPath is the agent.yaml a runner reads when it is given no --config.
func DefaultPath() (string, error) {
	return fssecure.ConfigFile("agent.yaml")
}
