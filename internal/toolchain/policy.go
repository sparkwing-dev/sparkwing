// Package toolchain contains policy shared by Sparkwing executable selectors.
package toolchain

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
)

// Mode controls whether a required release may be fetched.
type Mode string

const (
	// ModeAuto fetches a missing required release.
	ModeAuto Mode = "auto"
	// ModeLocal restricts selection to binaries already supplied by the operator.
	ModeLocal Mode = "local"
)

// ParseMode accepts the SPARKWING_TOOLCHAIN grammar.
func ParseMode(raw string) (Mode, error) {
	switch strings.TrimSpace(raw) {
	case "", string(ModeAuto):
		return ModeAuto, nil
	case string(ModeLocal):
		return ModeLocal, nil
	default:
		return "", fmt.Errorf("unknown toolchain mode %q", raw)
	}
}

// Hold is an operator's maximum permitted release and its source.
type Hold struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

// ResolveHold reads the hold file at configPath; an absent or empty file is
// no hold.
func ResolveHold(configPath string) (Hold, error) {
	body, err := readHoldFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return Hold{}, nil
	}
	if err != nil {
		return Hold{Source: configPath}, fmt.Errorf("read operator hold: %w", err)
	}
	value := strings.TrimSpace(string(body))
	if value == "" {
		return Hold{}, nil
	}
	return Hold{Value: value, Source: configPath}, nil
}

// resolveHoldStrict distinguishes an invalid configured ceiling from the
// absence of one so unattended updaters can fail closed.
func resolveHoldStrict(configPath string) (Hold, error) {
	hold, err := ResolveHold(configPath)
	if err != nil {
		return hold, err
	}
	if hold.Value == "" {
		return hold, nil
	}
	if !validHold(hold.Value) {
		return hold, fmt.Errorf("invalid version hold %q from %s: use vMAJOR.MINOR or vMAJOR.MINOR.PATCH", hold.Value, hold.Source)
	}
	return hold, nil
}

// validHold reports whether value is a stable minor-series or exact release ceiling.
func validHold(value string) bool {
	value = strings.TrimSpace(value)
	if buildinfo.IsReleaseVersion(value) {
		return true
	}
	return strings.Count(strings.TrimPrefix(value, "v"), ".") == 1 &&
		buildinfo.IsReleaseVersion(value+".0")
}

// ExceedsHold reports whether target crosses an exact or minor-series ceiling.
func ExceedsHold(target, hold string) bool {
	target = strings.TrimSpace(target)
	hold = strings.TrimSpace(hold)
	if hold == "" || !semver.IsValid(target) || !semver.IsValid(hold) {
		return false
	}
	if holdHasPatch(hold) {
		return semver.Compare(target, hold) > 0
	}
	return semver.Compare(semver.MajorMinor(target), semver.MajorMinor(hold)) > 0
}

func holdHasPatch(hold string) bool {
	core := strings.TrimPrefix(hold, "v")
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		core = core[:i]
	}
	return strings.Count(core, ".") >= 2
}
