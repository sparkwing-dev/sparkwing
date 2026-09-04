// Package toolchain contains policy shared by Sparkwing executable selectors.
package toolchain

import (
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

// Action is the result of selecting an executable for a required release.
type Action int

const (
	// Stay keeps the executable already running.
	Stay Action = iota
	// Switch runs the required release from the verified toolchain store.
	Switch
	// Refuse reports that local-only policy cannot serve the requirement.
	Refuse
)

// Selection describes the versions and operator policy that govern selection.
type Selection struct {
	Installed   string
	Required    string
	Replacement string
	Active      string
	Mode        Mode
}

// Select decides whether a stable required release outranks the running one.
func Select(selection Selection) Action {
	switch {
	case selection.Active != "" && selection.Active == selection.Required:
		return Stay
	case selection.Replacement != "" || selection.Required == "":
		return Stay
	case !buildinfo.IsReleaseVersion(selection.Installed) || !buildinfo.IsReleaseVersion(selection.Required):
		return Stay
	case semver.Compare(selection.Installed, selection.Required) >= 0:
		return Stay
	case selection.Mode == ModeLocal:
		return Refuse
	default:
		return Switch
	}
}

// Hold is an operator's maximum permitted release and its source.
type Hold struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

// ResolveHold gives an explicit environment setting precedence over the config file.
func ResolveHold(environment Hold, configPath string) Hold {
	if value := strings.TrimSpace(environment.Value); value != "" {
		return Hold{Value: value, Source: environment.Source}
	}
	body, err := os.ReadFile(configPath)
	if err != nil {
		return Hold{}
	}
	value := strings.TrimSpace(string(body))
	if value == "" {
		return Hold{}
	}
	return Hold{Value: value, Source: configPath}
}

// ResolveHoldStrict distinguishes an invalid configured ceiling from the
// absence of one so unattended updaters can fail closed.
func ResolveHoldStrict(environment Hold, configPath string) (Hold, error) {
	hold := ResolveHold(environment, configPath)
	if hold.Value == "" {
		return hold, nil
	}
	if !ValidHold(hold.Value) {
		return hold, fmt.Errorf("invalid version hold %q from %s: use vMAJOR.MINOR or vMAJOR.MINOR.PATCH", hold.Value, hold.Source)
	}
	return hold, nil
}

// ValidHold reports whether value is a stable minor-series or exact release ceiling.
func ValidHold(value string) bool {
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
