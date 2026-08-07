package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// triggerLine is one rendered trigger: what fires the pipeline, and
// whatever stands between the declaration and it actually firing.
type triggerLine struct {
	Event string `json:"event"`
	// Detail is the trigger's own configuration, e.g. a cron
	// expression, rendered for a reader.
	Detail string `json:"detail,omitempty"`
	// Blocker is what the declaration still needs from outside the
	// repo. Empty when nothing does.
	Blocker string `json:"blocker,omitempty"`
	// Advisory names fields that record intent and are not gated on.
	Advisory string `json:"advisory,omitempty"`
}

// describeTriggers renders a pipeline's `on:` block.
//
// A trigger is the half of a pipeline's identity that the DAG does not
// show, and its absence is invisible: an agent trial produced a pipeline
// for "run go test on pull requests" that declared none, and it passed
// lint, passed explain, ran green, and could never fire. Every signal
// available said it was the best result in that sweep.
//
// This reports rather than judges. A manual-only pipeline is a
// legitimate thing to want, so nothing here is a finding -- which is
// also why it belongs in explain and not in the linter, where it would
// have to guess at intent to decide whether to fail a build.
func describeTriggers(on pipelines.Triggers) []triggerLine {
	var out []triggerLine

	// Filters that record intent without being enforced are the quiet
	// half of this: `branches: [main]` reads like it scopes the trigger,
	// and today nothing outside the type definition reads it.
	//
	// The delivery note says what explain knows and names what answers
	// the rest. An earlier version asserted "not yet live", which is a
	// claim about GitHub that this command never checked -- it reads as
	// a warning to someone whose webhook is configured correctly.
	// `cluster webhooks list` does check, by deriving the pipeline from
	// each hook's URL path, but it shells out to `gh` over the network:
	// explain is offline and `explain --all` is a CI gate, so consulting
	// it here would trade a fast local check for a flaky remote one.
	const webhookBlocker = "delivery depends on a GitHub webhook posting to this pipeline"

	// Git hooks are the one case explain could check locally, and
	// `pipeline hooks status` already reports declared vs installed --
	// so point at it rather than duplicating its answer badly.
	const hookBlocker = "fires only once the git hook is installed"

	if t := on.Push; t != nil {
		out = append(out, triggerLine{
			Event:    "push",
			Blocker:  webhookBlocker,
			Advisory: advisoryFields("branches", len(t.Branches) > 0, "paths", len(t.Paths) > 0),
		})
	}
	if t := on.PullRequest; t != nil {
		out = append(out, triggerLine{
			Event:    "pull_request",
			Detail:   "opened / synchronize / reopened",
			Blocker:  webhookBlocker,
			Advisory: advisoryFields("branches", len(t.Branches) > 0, "actions", len(t.Actions) > 0),
		})
	}
	if on.Schedule != "" {
		out = append(out, triggerLine{
			Event:   "schedule",
			Detail:  on.Schedule + " (UTC)",
			Blocker: "fires only while a controller is running to evaluate the cron",
		})
	}
	if t := on.Webhook; t != nil {
		out = append(out, triggerLine{Event: "webhook", Detail: t.Path})
	}
	if on.PreHook != nil {
		out = append(out, triggerLine{Event: "pre_commit", Blocker: hookBlocker})
	}
	if on.PostHook != nil {
		out = append(out, triggerLine{Event: "pre_push", Blocker: hookBlocker})
	}
	if on.PostCommitHook != nil {
		out = append(out, triggerLine{Event: "post_commit", Blocker: hookBlocker})
	}
	return out
}

// advisoryFields names the declared-but-unenforced filters, given
// alternating (name, declared) pairs.
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

// pipelineTriggers looks up one pipeline's declared triggers. A missing
// project or a missing pipeline yields no triggers and no error: explain
// already ran the pipeline binary successfully by this point, so a
// config it cannot read is a reason to say less, not to fail.
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

// printTriggers renders the trigger section of `pipeline explain`.
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
	// Blockers are hoisted below the list and de-duplicated: push and
	// pull_request share one, and repeating a two-line caveat per
	// trigger buries the triggers themselves.
	var blockers []string
	seen := map[string]bool{}
	for _, l := range lines {
		head := "  " + color.Bold(l.Event)
		if l.Detail != "" {
			head += "  " + color.Dim(l.Detail)
		}
		fmt.Println(head)
		if l.Advisory != "" {
			fmt.Printf("    %s\n", color.Dim(l.Advisory))
		}
		if l.Blocker != "" && !seen[l.Blocker] {
			seen[l.Blocker] = true
			blockers = append(blockers, l.Blocker)
		}
	}
	if len(blockers) == 0 {
		return
	}
	fmt.Println()
	for _, b := range blockers {
		fmt.Printf("  %s\n", color.Dim(b))
	}
	// Name the command that actually answers it. explain cannot: the
	// check shells out to `gh` over the network, and this command is
	// offline and gates CI via --all.
	var steps []InfoNextStep
	if seen["delivery depends on a GitHub webhook posting to this pipeline"] {
		steps = append(steps, InfoNextStep{
			Command: "sparkwing cluster webhooks list --repo OWNER/NAME",
			Purpose: "which hooks point at which pipeline",
		})
	}
	if seen["fires only once the git hook is installed"] {
		steps = append(steps, InfoNextStep{
			Command: "sparkwing pipeline hooks status",
			Purpose: "declared, installed, and missing hooks",
		})
	}
	if len(steps) > 0 {
		printAlignedSteps(steps)
	}
}
