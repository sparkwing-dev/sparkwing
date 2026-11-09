package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func checkTrustedFlags(flags triggerFlags) error {
	if !flags.trusted {
		return nil
	}
	for _, conflict := range []struct {
		flag string
		set  bool
	}{{"--runner", flags.runner != ""}, {"--working-tree", flags.workingTree}, {"--ref", flags.ref != ""}, {"--pipeline-ref", flags.pipelineRef != ""}} {
		if conflict.set {
			return fmt.Errorf("--trusted runs HEAD on this machine's own runner, so it refuses %s", conflict.flag)
		}
	}
	return nil
}

// safety: trustedRunner only refuses early what would not count. The controller alone decides whether a
// pass counts: it fetches the commit, records each claim's host trust and seals proof eligibility, so a
// check this CLI skips or gets wrong yields a run that does not count, never one that counts wrongly.
func trustedRunner(ctx context.Context, controllerURL, configPath, checkout string) (string, error) {
	refuse := func(message, hint string) (string, error) {
		return "", &apierr.Error{Code: apierr.InvalidArgs, Message: "--trusted: " + message, Hint: hint}
	}
	section, err := userconfig.Node(configPath, userconfig.Runner)
	if err != nil {
		return "", err
	}
	var runner userconfig.RunnerConfig
	if section != nil {
		if err := section.Decode(&runner); err != nil {
			return "", err
		}
	}
	enroll := "enroll this machine with `sparkwing runners add --runner NAME --repos OWNER/REPO --profile PROFILE`"
	if runner.Controller == nil || runner.HolderPrefix == nil || *runner.HolderPrefix == "" || strings.TrimRight(*runner.Controller, "/") != strings.TrimRight(controllerURL, "/") {
		return refuse("this machine has no runner enrolled with the profile's controller", enroll)
	}
	name := *runner.HolderPrefix
	if len(runner.Repos) == 0 {
		return refuse("runner "+name+" is not scoped to repositories with --repos, so its passes never count", enroll)
	}
	git := triggerCheckout{directory: checkout, environment: os.Environ()}
	status, err := git.read(ctx, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status) != "" {
		return refuse("the checkout has uncommitted changes, so HEAD's tree is not what the gate would judge", "commit or stash them, push, and run again")
	}
	branch, err := git.read(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return refuse("HEAD is not on a branch", "check out the branch you pushed")
	}
	head, headErr := git.read(ctx, "rev-parse", "--verify", "--quiet", "HEAD")
	pushed, pushedErr := git.read(ctx, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	if err := errors.Join(headErr, pushedErr); err != nil || head != pushed {
		return refuse("HEAD is not the head of origin/"+branch+", so the controller cannot fetch and prove it", "run `git push origin "+branch+"` and run again")
	}
	return name, nil
}
