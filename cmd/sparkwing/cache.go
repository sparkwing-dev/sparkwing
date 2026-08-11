package main

import (
	"errors"
	"os"
)

var errCacheCommandUnavailable = errors.New("pipeline cache pressure command unavailable")

func runCache(args []string) error {
	if handleParentHelp(cmdCache, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdCache, os.Stderr)
		return errors.New("cache: subcommand required (status|prune)")
	}
	switch args[0] {
	case "status":
		return runCacheStatus(args[1:])
	case "prune":
		return runCachePrune(args[1:])
	default:
		PrintHelp(cmdCache, os.Stderr)
		return errors.New("cache: unknown subcommand " + args[0])
	}
}

func runCacheStatus([]string) error {
	return errCacheCommandUnavailable
}

func runCachePrune([]string) error {
	return errCacheCommandUnavailable
}
