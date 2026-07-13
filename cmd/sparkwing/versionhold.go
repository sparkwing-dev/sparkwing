// A version hold is an operator-set ceiling on CLI self-upgrades that
// the tool enforces: `sparkwing version hold --set v0.15` persists a
// hold in the user config, and `sparkwing version update --cli` (and
// `sparkwing update`) refuse to cross it. Instructions do not bind an
// agent; a tool-enforced ceiling does. The hold is deliberately hard
// to reach around: an explicit override exists but is never named in
// the refusal message.
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

// versionHoldEnv is the environment override for the version hold. It
// takes precedence over the on-disk config so an operator can pin a
// ceiling for a single shell or a whole fleet without editing files.
const versionHoldEnv = "SPARKWING_VERSION_HOLD"

// versionHold is the resolved upgrade ceiling: the value plus where it
// came from, so the refusal and `sparkwing version` can name the
// operator setting an agent must not reach around.
type versionHold struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

// resolveVersionHold reads the effective hold: the environment
// override wins, else the on-disk config file. A zero versionHold
// (empty Value) means no hold is set.
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

// versionHoldPath is the on-disk hold file, honoring
// XDG_CONFIG_HOME > $HOME/.config, matching profiles.yaml's location.
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

// normalizeHold canonicalizes forgiving input into a valid hold value
// or returns an error naming the accepted shapes. A bare "0.15" gains
// its leading v; the value must be a valid semver ceiling (vMAJOR.MINOR
// caps a whole minor series, vMAJOR.MINOR.PATCH is an exact ceiling).
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

// holdHasPatch reports whether a hold value pins a patch (vX.Y.Z),
// making it an exact ceiling rather than a minor-series cap (vX.Y).
func holdHasPatch(hold string) bool {
	core := strings.TrimPrefix(hold, "v")
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		core = core[:i]
	}
	return strings.Count(core, ".") >= 2
}

// exceedsHold reports whether upgrading to target would cross the hold
// ceiling. A minor-series hold (vX.Y) caps the whole series: any patch
// of that minor is allowed, the next minor is refused. A patch-pinned
// hold (vX.Y.Z) is an exact ceiling. A missing or malformed hold, or an
// unparseable target, never blocks.
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

// holdRefusal is the error a blocked upgrade returns. It names the
// operator setting and where it lives so an agent reports the block to
// its operator -- and deliberately does NOT mention the override flag,
// which no agent should reach for.
func holdRefusal(target string, hold versionHold) error {
	return fmt.Errorf(
		"update refused: an operator set a version hold at %s (%s), and %s is beyond it.\n"+
			"  This ceiling is deliberate. Report the block to the operator who set the hold;\n"+
			"  do not work around it.",
		hold.Value, hold.Source, target)
}

// runVersionHold implements `sparkwing version hold`: show, --set, or
// --clear the operator upgrade ceiling.
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
