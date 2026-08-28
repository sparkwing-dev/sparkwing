package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	flag "github.com/spf13/pflag"
	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

const versionHoldEnv = "SPARKWING_VERSION_HOLD"

type versionHold struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

func resolveVersionHold() versionHold {
	if v := strings.TrimSpace(os.Getenv(versionHoldEnv)); v != "" {
		return versionHold{Value: v, Source: versionHoldEnv}
	}
	path, err := versionHoldPath()
	if err != nil {
		return versionHold{}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return versionHold{}
	}
	v := strings.TrimSpace(string(body))
	if v == "" {
		return versionHold{}
	}
	return versionHold{Value: v, Source: path}
}

func versionHoldPath() (string, error) {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "sparkwing", "version-hold"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve version-hold path: %w", err)
	}
	return filepath.Join(home, ".config", "sparkwing", "version-hold"), nil
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
	t := strings.TrimSpace(target)
	h := strings.TrimSpace(hold)
	if h == "" || !semver.IsValid(t) || !semver.IsValid(h) {
		return false
	}
	if holdHasPatch(h) {
		return semver.Compare(t, h) > 0
	}
	return semver.Compare(semver.MajorMinor(t), semver.MajorMinor(h)) > 0
}

func holdRefusal(target string, hold versionHold) error {
	return fmt.Errorf(
		"update refused: an operator set a version hold at %s (%s), and %s is beyond it.\n"+
			"  This ceiling is deliberate. Report the block to the operator who set the hold;\n"+
			"  do not work around it",
		hold.Value, hold.Source, target)
}

func runVersionHold(args []string) error {
	fs := flag.NewFlagSet(cmdVersionHold.Path, flag.ContinueOnError)
	set := fs.String("set", "", "set the hold to this version ceiling (e.g. v0.15 or v0.15.4)")
	clear := fs.Bool("clear", false, "remove the hold so upgrades are unrestricted")
	if err := parseAndCheck(cmdVersionHold, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("version hold: unexpected positional %q", fs.Arg(0))
	}
	setChanged := fs.Changed("set")
	if setChanged && *clear {
		return errors.New("version hold: --set and --clear are mutually exclusive")
	}

	switch {
	case setChanged:
		value, err := normalizeHold(*set)
		if err != nil {
			return err
		}
		path, err := versionHoldPath()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create config dir: %w", err)
		}
		if err := os.WriteFile(path, []byte(value+"\n"), 0o644); err != nil {
			return fmt.Errorf("write hold: %w", err)
		}
		fmt.Printf("version hold set to %s (%s)\n", value, path)
		if env := strings.TrimSpace(os.Getenv(versionHoldEnv)); env != "" && env != value {
			fmt.Printf("note: %s=%s is set and overrides this file for the current shell\n", versionHoldEnv, env)
		}
		return nil
	case *clear:
		path, err := versionHoldPath()
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clear hold: %w", err)
		}
		fmt.Println("version hold cleared")
		if env := strings.TrimSpace(os.Getenv(versionHoldEnv)); env != "" {
			fmt.Printf("note: %s=%s is still set and holds upgrades for the current shell\n", versionHoldEnv, env)
		}
		return nil
	default:
		hold := resolveVersionHold()
		if hold.Value == "" {
			fmt.Println(color.Dim("no version hold set (CLI upgrades are unrestricted)"))
			return nil
		}
		fmt.Printf("held at %s by operator (%s)\n", hold.Value, hold.Source)
		return nil
	}
}
