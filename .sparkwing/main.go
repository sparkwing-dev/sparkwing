package main

import (
	"fmt"
	"os"
	"path/filepath"
	_ "sparkwing-pipelines/jobs"

	"github.com/sparkwing-dev/sparkwing/pkg/runner"
)

func main() {
	if err := requireReleaseHome(os.Args, os.Getenv, os.UserHomeDir); err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(2)
	}
	runner.Main()
}

func requireReleaseHome(args []string, getenv func(string) string, userHomeDir func() (string, error)) error {
	if len(args) < 2 || args[1] != "release" {
		return nil
	}
	home := getenv("SPARKWING_HOME")
	if home == "" {
		return fmt.Errorf("SPARKWING_HOME must name an isolated directory; a release build must not migrate the operational runs store")
	}
	userHome, err := userHomeDir()
	if err != nil {
		return fmt.Errorf("resolve the operational Sparkwing home: %w", err)
	}
	if filepath.Clean(home) == filepath.Join(userHome, ".sparkwing") {
		return fmt.Errorf("SPARKWING_HOME %q is the operational runs store; choose an isolated release directory", home)
	}
	return nil
}
