package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const hereRunnerFlag = "here"

func checkHereFlags(flags triggerFlags) error {
	switch {
	case flags.runner != hereRunnerFlag && flags.reason != "":
		return errors.New("--reason is recorded only with --runner here")
	case flags.runner != hereRunnerFlag:
		return nil

	case strings.TrimSpace(flags.reason) == "":
		return errors.New("--runner here needs --reason TEXT, which the run records")
	case flags.detach:
		return errors.New("--runner here hosts the run in this process, so it cannot --detach")
	case flags.workingTree:
		return errors.New("--runner here builds this checkout as it is; commit the change instead of passing --working-tree")
	}
	return nil
}

// safety: the run records the commit HEAD names, so a tracked change that
// would run without being recorded is refused.
func refuseUncommittedCheckout() error {
	out, err := gitOutput(context.Background(), ".", nil, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return fmt.Errorf("pipeline trigger: read the checkout's status: %w", err)
	}
	if strings.TrimSpace(out) != "" {
		return errors.New("pipeline trigger: --runner here builds this checkout and the run records HEAD, " +
			"so commit the tracked changes first")
	}
	return nil
}
