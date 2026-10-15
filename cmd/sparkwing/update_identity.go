package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// safety: a signature covers bytes, not a tag, so an older signed release
// served under a newer tag passes verification; only the binary's own report
// catches that substitution.
func assertReleaseVersion(binPath, version string) error {
	args := []string{"version", "-o", "json", "--offline"}
	// #nosec G702 -- the release binary this process just verified against its signed manifest, asked only for its version
	out, err := exec.Command(binPath, args...).Output()
	if err != nil {
		return fmt.Errorf("ask the fetched sparkwing %s for its version: `%s %s`: %w%s",
			version, binPath, strings.Join(args, " "), err, childStderr(err))
	}
	var report VersionReport
	if err := json.Unmarshal(out, &report); err != nil {
		return fmt.Errorf("parse the version the fetched sparkwing %s reports: %w", version, err)
	}
	if report.CLI.Installed != version {
		return fmt.Errorf(
			"the release published as %s reports itself as %s; refusing it",
			version, report.CLI.Installed)
	}
	return nil
}

func childStderr(err error) string {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return ""
	}
	text := strings.TrimSpace(string(exitErr.Stderr))
	if text == "" {
		return ""
	}
	return "\n  it printed: " + text
}
