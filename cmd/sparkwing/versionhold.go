package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/configguard"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	sharedtoolchain "github.com/sparkwing-dev/sparkwing/internal/toolchain"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

type versionHold struct {
	Value  string `json:"value"`
	Source string `json:"source"`
	Error  string `json:"error,omitempty"`
}

func resolveVersionHold() versionHold {
	path, err := versionHoldPath()
	if err != nil {
		return versionHold{Source: "version-hold configuration", Error: fmt.Sprintf("resolve operator hold: %v", err)}
	}
	hold, readErr := sharedtoolchain.ResolveHold(path)
	report := versionHold{Value: hold.Value, Source: hold.Source}
	if readErr != nil {
		report.Error = readErr.Error()
		return report
	}
	if report.Value != "" {
		normalized, err := normalizeHold(report.Value)
		if err != nil {
			report.Error = fmt.Sprintf("invalid version hold %q (%s)", report.Value, report.Source)
		} else {
			report.Value = normalized
		}
	}
	return report
}

func versionHoldPath() (string, error) {
	return fssecure.ConfigFile("version-hold")
}

// safety: the hold file is machine-wide while the toolchains it governs live
// under SPARKWING_HOME, so a command under a scratch home that wrote or
// removed it would change every other shell's ceiling unannounced.
func guardVersionHoldWrite(path string) error {
	return configguard.GuardWrite("the version hold", "", path)
}

func normalizeHold(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", errors.New("hold --set: value required (e.g. --set v0.15 or --set v0.15.4)")
	}
	if !strings.HasPrefix(v, "v") && semver.IsValid("v"+v) {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return "", fmt.Errorf("hold --set: %q is not a valid version; expected vMAJOR.MINOR (e.g. v0.15) or vMAJOR.MINOR.PATCH (e.g. v0.15.4)", raw)
	}
	return v, nil
}

func holdHasPatch(hold string) bool {
	core := strings.TrimPrefix(hold, "v")
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		core = core[:i]
	}
	return strings.Count(core, ".") >= 2
}

func exceedsHold(target, hold string) bool {
	return sharedtoolchain.ExceedsHold(target, hold)
}

func holdRefusal(target string, hold versionHold) error {
	return fmt.Errorf(
		"update refused: an operator set a version hold at %s (%s), and %s is beyond it.\n"+
			"  This ceiling is deliberate. Report the block to the operator who set the hold;\n"+
			"  do not work around it",
		hold.Value, hold.Source, target)
}

func runVersionHold(inv clisurface.Invocation) error {
	switch {
	case inv.Has("set"):
		value, err := normalizeHold(inv.Value("set"))
		if err != nil {
			return &clisurface.UsageError{Path: inv.Command.Path, Message: err.Error()}
		}
		path, err := versionHoldPath()
		if err != nil {
			return err
		}
		if err := guardVersionHoldWrite(path); err != nil {
			return err
		}
		if err := fssecure.EnsureConfigDir(filepath.Dir(path)); err != nil {
			return fmt.Errorf("create config dir: %w", err)
		}
		if err := fssecure.WriteFile(path, []byte(value+"\n")); err != nil {
			return fmt.Errorf("write hold: %w", err)
		}
	case inv.Bool("clear"):
		path, err := versionHoldPath()
		if err != nil {
			return err
		}
		if err := guardVersionHoldWrite(path); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clear hold: %w", err)
		}
	}
	hold := resolveVersionHold()
	if hold.Error != "" {
		return errors.New(hold.Error)
	}
	switch inv.Output(color.IsInteractiveStdout()) {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(hold)
	case "plain":
		_, err := fmt.Fprintln(os.Stdout, hold.Value)
		return err
	}
	if hold.Value == "" {
		_, err := fmt.Fprintln(os.Stdout, "no version hold set (CLI upgrades are unrestricted)")
		return err
	}
	_, err := fmt.Fprintf(os.Stdout, "held at %s by operator (%s)\n", hold.Value, hold.Source)
	return err
}
