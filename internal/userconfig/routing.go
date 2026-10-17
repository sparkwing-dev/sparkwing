package userconfig

import (
	"math"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// RunRouting selects ownership and overflow without granting controller authority.
type RunRouting struct {
	Route           *string           `yaml:"route,omitempty" json:"route,omitempty"`
	Profile         *string           `yaml:"profile,omitempty" json:"profile,omitempty"`
	Prefer          []RoutePreference `yaml:"prefer,omitempty" json:"prefer,omitempty"`
	OverflowMinutes *int64            `yaml:"overflow_minutes,omitempty" json:"overflow_minutes,omitempty"`
	LocalSecrets    []string          `yaml:"local_secrets,omitempty" json:"local_secrets,omitempty"`
}

// RoutePreference delays later eligible claimants by the configured hold.
type RoutePreference struct {
	Target string `yaml:"target" json:"target"`
	Hold   string `yaml:"hold,omitempty" json:"hold,omitempty"`
}

// ValidateRunRouting validates explicit configuration without filling defaults.
func ValidateRunRouting(key string, r *RunRouting) error {
	if r == nil {
		return nil
	}
	if r.Route != nil && *r.Route != "local" && *r.Route != "cloud" {
		return Invalid(key+".route", "must be local or cloud")
	}
	if r.Profile != nil && strings.TrimSpace(*r.Profile) == "" {
		return Invalid(key+".profile", "must name a user-configured profile")
	}
	if r.OverflowMinutes != nil && (*r.OverflowMinutes < 0 || *r.OverflowMinutes > math.MaxInt64/60) {
		return Invalid(key+".overflow_minutes", "must be non-negative")
	}
	if r.Prefer != nil && len(r.Prefer) == 0 {
		return Invalid(key+".prefer", "must name at least one claimant")
	}
	seen := map[string]bool{}
	for _, p := range r.Prefer {
		valid := p.Target == "here" || p.Target == "machines" || p.Target == "cloud"
		for _, prefix := range []string{"name=", "pool="} {
			if value, ok := strings.CutPrefix(p.Target, prefix); ok && strings.TrimSpace(value) != "" && !strings.ContainsAny(value, ", \t\r\n") {
				valid = true
			}
		}
		if !valid || seen[p.Target] {
			return Invalid(key+".prefer", "expected distinct here, machines, cloud, name=NAME or pool=POOL targets")
		}
		seen[p.Target] = true
		if p.Hold != "" {
			d, err := time.ParseDuration(p.Hold)
			if err != nil || d < 0 {
				return Invalid(key+".prefer.hold", "expected a non-negative duration")
			}
		}
	}
	for _, name := range r.LocalSecrets {
		if secrets.ValidateName(name) != nil {
			return Invalid(key+".local_secrets", "invalid secret name")
		}
	}
	return nil
}

// MergeRunRouting applies explicitly supplied fields from low to high precedence.
func MergeRunRouting(layers ...*RunRouting) RunRouting {
	var r RunRouting
	for _, next := range layers {
		if next == nil {
			continue
		}
		if next.Route != nil {
			r.Route = next.Route
		}
		if next.Profile != nil {
			r.Profile = next.Profile
		}
		if next.Prefer != nil {
			r.Prefer = append([]RoutePreference{}, next.Prefer...)
		}
		if next.OverflowMinutes != nil {
			r.OverflowMinutes = next.OverflowMinutes
		}
		if next.LocalSecrets != nil {
			r.LocalSecrets = append([]string{}, next.LocalSecrets...)
		}
	}
	return r
}
