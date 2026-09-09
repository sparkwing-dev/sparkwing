package main

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

type triggerLine struct {
	Event string `json:"event"`

	Detail string `json:"detail,omitempty"`

	Advisory string `json:"advisory,omitempty"`
}

func describeTriggers(on pipelines.Triggers) []triggerLine {
	var out []triggerLine

	if t := on.Push; t != nil {
		out = append(out, triggerLine{
			Event:    "push",
			Advisory: advisoryFields("branches", len(t.Branches) > 0, "paths", len(t.Paths) > 0),
		})
	}
	if t := on.PullRequest; t != nil {
		out = append(out, triggerLine{
			Event:    "pull_request",
			Detail:   "opened / synchronize / reopened",
			Advisory: advisoryFields("branches", len(t.Branches) > 0, "actions", len(t.Actions) > 0),
		})
	}
	for i := range on.Schedule {
		out = append(out, triggerLine{
			Event:  "schedule",
			Detail: scheduleDetail(&on.Schedule[i]),
		})
	}
	if t := on.Webhook; t != nil {
		out = append(out, triggerLine{Event: "webhook", Detail: t.Path})
	}
	if on.PreHook != nil {
		out = append(out, triggerLine{Event: "pre_commit"})
	}
	if on.PostHook != nil {
		out = append(out, triggerLine{Event: "pre_push"})
	}
	if on.PostCommitHook != nil {
		out = append(out, triggerLine{Event: "post_commit"})
	}
	return out
}

func scheduleDetail(t *pipelines.ScheduleTrigger) string {
	tz := t.TZ
	if tz == "" {
		tz = pipelines.DefaultScheduleTZ
	}
	detail := fmt.Sprintf("%s: %s (%s) where %s", t.EffectiveName(), t.Cron, tz, t.Where)
	var extra []string
	if policy := t.OverlapPolicy(); policy != pipelines.DefaultScheduleOverlap {
		extra = append(extra, "overlap "+policy)
	}
	if window, err := t.CatchUpDuration(); err == nil && window != pipelines.DefaultScheduleCatchUp {
		extra = append(extra, "catch-up "+window.String())
	}
	for _, key := range slices.Sorted(maps.Keys(t.Args)) {
		extra = append(extra, "arg "+key+"="+t.Args[key])
	}
	if len(extra) > 0 {
		detail += ", " + strings.Join(extra, ", ")
	}
	return detail
}

func advisoryFields(pairs ...any) string {
	var names []string
	for i := 0; i+1 < len(pairs); i += 2 {
		name, _ := pairs[i].(string)
		if declared, _ := pairs[i+1].(bool); declared {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return strings.Join(names, " + ") + " records intent; not gated on today"
}

func pipelineTriggers(name string) (pipelines.Triggers, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return pipelines.Triggers{}, false
	}
	_, cfg, err := projectconfig.DiscoverPipelines(cwd)
	if err != nil || cfg == nil {
		return pipelines.Triggers{}, false
	}
	for _, p := range cfg.Pipelines {
		if p.Name == name {
			return p.On, true
		}
	}
	return pipelines.Triggers{}, false
}

func printTriggers(name string) {
	on, found := pipelineTriggers(name)
	if !found {
		return
	}
	lines := describeTriggers(on)
	if len(lines) == 0 {
		fmt.Printf("Triggers: %s\n", color.Dim("none -- runs only when invoked (`sparkwing run "+name+"`)"))
		fmt.Printf("          %s\n", color.Dim("declare one with the `on:` block in .sparkwing/sparkwing.yaml"))
		return
	}
	fmt.Println("Triggers:")
	for _, l := range lines {
		head := "  " + color.Bold(l.Event)
		if l.Detail != "" {
			head += "  " + color.Dim(l.Detail)
		}
		fmt.Println(head)
		if l.Advisory != "" {
			fmt.Printf("    %s\n", color.Dim(l.Advisory))
		}
	}
}
