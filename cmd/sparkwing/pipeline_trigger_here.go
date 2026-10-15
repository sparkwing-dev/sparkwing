package main

import (
	"errors"
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
