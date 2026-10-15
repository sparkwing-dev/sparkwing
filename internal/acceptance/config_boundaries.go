package acceptance

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// ConfigBoundaryScenarios identifies executable parser proofs. Placement drivers
// still own file acquisition, intake and resolved-setting consumption.
func ConfigBoundaryScenarios() []string {
	return []string{"surface-config-decoder-boundaries", "surface-config-final-schema"}
}

// CheckConfigBoundary exercises the shared decoders against independent fixtures.
func CheckConfigBoundary(scenarioID string) error {
	switch scenarioID {
	case "surface-config-decoder-boundaries":
		return configDecoderBoundaries()
	case "surface-config-final-schema":
		return configFinalSchema()
	default:
		return fmt.Errorf("no config boundary proof for %q", scenarioID)
	}
}

func configDecoderBoundaries() error {
	for _, test := range []struct {
		raw, key string
		machine  bool
	}{
		{"defaults: {args: {x: first, x: second}}", "defaults.args.x", false},
		{"defaults: {requires: null}", "defaults.requires", false},
		{"{}\n---\n{}", "$", false},
		{"cache: {max_entries: 1, max_entries: 2}", "cache.max_entries", true},
		{"run: {submit_env_allow: null}", "run.submit_env_allow", true},
		{"{}\n---\n{}", "$", true},
	} {
		if err := refuseConfig(test.raw, test.key, test.machine); err != nil {
			return err
		}
	}
	absent, err := projectconfig.Parse([]byte("{}"))
	if err != nil {
		return err
	}
	empty, err := projectconfig.Parse([]byte("defaults: {args: {}, requires: [], prefers: [], guards: {when: [], unless: []}}\npipelines: [{name: build, entrypoint: Build, description: '', hidden: false, args: {}, requires: []}]"))
	if err != nil {
		return err
	}
	if absent.Defaults != nil || absent.Pipelines != nil || empty.Defaults == nil || empty.Defaults.Args == nil || empty.Defaults.Requires == nil || empty.Defaults.Prefers == nil || empty.Defaults.Guards == nil || empty.Defaults.Guards.When == nil || empty.Defaults.Guards.Unless == nil {
		return errors.New("project decoder lost absent/empty distinction")
	}
	pipeline := empty.Pipelines[0]
	if pipeline.Description == nil || *pipeline.Description != "" || pipeline.Hidden == nil || *pipeline.Hidden || pipeline.Args == nil || pipeline.Requires == nil {
		return errors.New("pipeline decoder lost explicit empty/false")
	}
	machineAbsent, err := userconfig.Decode("config.yaml", []byte("{}"))
	if err != nil {
		return err
	}
	machineEmpty, err := userconfig.Decode("config.yaml", []byte("profiles: {}\nrun: {submit_env_allow: [], cache_from: ''}\ncache: {max_bytes: '0', max_entries: 0}\nrunner: {local_admission: false, repos: []}"))
	if err != nil {
		return err
	}
	if machineAbsent.Profiles != nil || machineAbsent.Run != nil || machineEmpty.Profiles == nil || machineEmpty.Run.SubmitEnvAllow == nil || machineEmpty.Cache.MaxEntries == nil || *machineEmpty.Cache.MaxEntries != 0 || machineEmpty.Cache.MaxBytes == nil || *machineEmpty.Cache.MaxBytes != "0" || machineEmpty.Run.CacheFrom == nil || *machineEmpty.Run.CacheFrom != "" || machineEmpty.Runner.LocalAdmission == nil || *machineEmpty.Runner.LocalAdmission || machineEmpty.Runner.Repos == nil {
		return errors.New("machine decoder lost absent/empty/zero/false distinction")
	}
	return nil
}

func refuseConfig(raw, key string, machine bool) error {
	var err error
	if machine {
		_, err = userconfig.Decode("config.yaml", []byte(raw))
	} else {
		_, err = projectconfig.Parse([]byte(raw))
	}
	if err == nil {
		return fmt.Errorf("accepted invalid config at %s", key)
	}
	var field *userconfig.FieldError
	if !errors.As(err, &field) || field.Key != key || field.File == "" || field.Line < 1 {
		return fmt.Errorf("missing source location for %s: %w", key, err)
	}
	if strings.Contains(err.Error(), "boundary-secret-token") {
		return errors.New("configuration error exposed a credential")
	}
	return nil
}

func configFinalSchema() error {
	projectRaw := []byte(`defaults:
  args: {target: release}
  guards: {when: [where:controller], unless: ["arg:skip=true"]}
  requires: [pool=prod]
  prefers: [arch=arm64]
pipelines:
  - name: deploy
    entrypoint: Deploy
    source: origin/main
    profile: prod
    args: {region: west}
    guards: {when: ["git:branch=default"]}
    on:
      push: {branches: [main], paths: ["src/**"]}
      pull_request: {actions: [opened], branches: [main]}
      schedule: [{cron: "0 * * * *", where: controller, tz: UTC, overlap: queue, catch_up: 24h}]
      pre_commit: {}
      pre_push: {}
      post_commit: {}
sparks: [{name: core, source: github.com/acme/core, version: v1.0.0}]
`)
	original := bytes.Clone(projectRaw)
	project, err := projectconfig.Parse(projectRaw)
	if err != nil {
		return err
	}
	if !bytes.Equal(projectRaw, original) {
		return errors.New("project decoder mutated source")
	}
	p := project.Pipelines[0]
	if project.Defaults.Args["target"] != "release" || !reflect.DeepEqual(project.Defaults.Requires, []string{"pool=prod"}) || !reflect.DeepEqual(project.Defaults.Prefers, []string{"arch=arm64"}) || p.Args["region"] != "west" || p.Source == nil || *p.Source != "origin/main" || p.Profile == nil || *p.Profile != "prod" || p.Guards.When[0] != "git:branch=default" || p.On.PreCommit == nil || p.On.PrePush == nil || p.On.PostCommit == nil || len(p.On.Schedule) != 1 || *p.On.Schedule[0].CatchUp != "24h" || project.Sparks[0].Version != "v1.0.0" {
		return errors.New("project final-schema values changed")
	}
	machineRaw := []byte(`profiles: {prod: {controller: {url: https://controller.example, token: boundary-secret-token}}}
repos: {repos: [{path: /work/repo}], fallback_paths: [/work], auto_register: true, include_worktrees: false}
admission: {mode: custom, budget: "50%,8gb", custom: {class_weight: {batch: 0}, aging_every: 0}, jev: {min_confidence: 0}}
runner: {controller: https://controller.example, credentials_dir: /private/runner, repos: [github.com/acme/repo], accept_working_tree: true, max_concurrent: 2, keep_free: "1,2gb", contribution: "50%,8gb", spawn_policy: return-to-queue, poll: 1s, lease: 1m, heartbeat: 10s}
cache: {max_bytes: 1GiB, max_entries: 0}
machine: {box_id: machine-one}
debug: {pause_timeout: 2m}
run: {submit_env_allow: [BUILD_REGION], cache_from: prod}
`)
	machine, err := userconfig.Decode("config.yaml", machineRaw)
	if err != nil {
		return err
	}
	if machine.Profiles["prod"].Controller.Token == nil || *machine.Profiles["prod"].Controller.Token != "boundary-secret-token" || *machine.Runner.KeepFree != "1,2gb" || machine.Repos.Repos[0].Path != "/work/repo" || *machine.Runner.MaxConcurrent != 2 || *machine.Run.CacheFrom != "prod" || *machine.Machine.BoxID != "machine-one" {
		return errors.New("machine final-schema values changed")
	}
	for _, test := range []struct {
		raw, key string
		machine  bool
	}{
		{"profiles: {prod: {controller: {token: boundary-secret-token}}}", "profiles", false},
		{"pipelines: [{name: build, entrypoint: Build, on: {webhook: {}}}]", "pipelines[0].on.webhook", false},
		{"profiles: {prod: {controller: {url: https://example.com}}, prod: {controller: {url: https://example.com}}}", "profiles.prod", true},
		{"agent: {}", "agent", true},
		{"logs: {}", "logs", true},
		{"runner: {gitcache: ignored}", "runner.gitcache", true},
		{"runner: {allow_repos: []}", "runner.allow_repos", true},
		{"runner: {local_reserve: '1,2gb'}", "runner.local_reserve", true},
		{"runner: {profile: prod}", "runner.profile", true},
		{"run: {dispatch_wait: 1m}", "run.dispatch_wait", true},
		{"profiles: {prod: {controller: {url: https://example.com}, backend: local}}", "profiles.prod.backend", true},
		{"runner: {max_concurrent: 0}", "runner.max_concurrent", true},
		{"debug: {pause_timeout: 0s}", "debug.pause_timeout", true},
	} {
		if err := refuseConfig(test.raw, test.key, test.machine); err != nil {
			return err
		}
	}
	env, err := procenv.Build(procenv.Plan, procenv.Base{Local: true, Captured: []string{"SPARKWING_WORKERS=99", "SPARKWING_NODE_TOKEN=boundary-secret-token", "SPARKWING_CACHE_URL=https://legacy.example"}}, procenv.Runtime{})
	if err != nil {
		return err
	}
	for _, assignment := range env {
		if strings.HasPrefix(assignment, "SPARKWING_WORKERS=") || strings.HasPrefix(assignment, "SPARKWING_NODE_TOKEN=") || strings.HasPrefix(assignment, "SPARKWING_CACHE_URL=") {
			return errors.New("removed engine environment name survived")
		}
	}
	return nil
}
