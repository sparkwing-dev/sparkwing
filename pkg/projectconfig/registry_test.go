package projectconfig_test

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func TestProjectDeclarationsReachRuntime(t *testing.T) {
	cfg, err := projectconfig.Parse([]byte(`defaults:
  args: {mode: default}
  requires: [linux]
  guards: {when: [where:controller]}
pipelines:
- name: build
  entrypoint: Build
  description: Build it
  hidden: true
  source: origin/main
  profile: prod
  args: {mode: pipeline}
  requires: []
  guards: {}
  on:
    push: {branches: [main]}
    pull_request: {actions: [opened]}
    pre_commit: {}
    pre_push: {}
    post_commit: {}
    schedule:
    - name: nightly
      cron: '0 3 * * *'
      where: controller
      tz: UTC
      overlap: queue
      catch_up: 2h
      args: {mode: schedule}
- name: check
  entrypoint: Check
`))
	if err != nil {
		t.Fatal(err)
	}
	registry := cfg.Registry()
	build := registry.Find("build")
	if build == nil || build.Description != "Build it" || !build.Hidden || build.Source != "origin/main" || build.Profile != "prod" || build.Args["mode"] != "pipeline" {
		t.Fatalf("lost pipeline declarations: %#v", build)
	}
	if build.Requires == nil || len(build.Requires) != 0 || !build.Guards.IsEmpty() {
		t.Fatalf("explicit empty replaced by defaults: %#v", build)
	}
	if build.On.Push == nil || build.On.PullRequest == nil || build.On.PreHook == nil || build.On.PostHook == nil || build.On.PostCommitHook == nil {
		t.Fatal("lost event declarations")
	}
	if !reflect.DeepEqual(build.On.Schedule, pipelines.ScheduleTriggers{{Name: "nightly", Cron: "0 3 * * *", Where: "controller", TZ: "UTC", Overlap: "queue", CatchUp: "2h", Args: map[string]string{"mode": "schedule"}}}) {
		t.Fatalf("schedule = %#v", build.On.Schedule)
	}
	check := registry.Find("check")
	if !reflect.DeepEqual(check.Requires, []string{"linux"}) || check.Args["mode"] != "default" {
		t.Fatalf("lost defaults: %#v", check)
	}
	if err := check.Guards.Evaluate("check", pipelines.GuardContext{ProfileIsLocal: true}); err == nil {
		t.Fatal("controller guard accepted local execution")
	}
	if err := check.Guards.Evaluate("check", pipelines.GuardContext{}); err != nil {
		t.Fatal(err)
	}
	build.Args["mode"] = "mutated"
	if cfg.Pipelines[0].Args["mode"] != "pipeline" {
		t.Fatal("runtime mutation changed declarations")
	}
}
