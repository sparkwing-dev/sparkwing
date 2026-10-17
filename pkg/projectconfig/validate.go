package projectconfig

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

var (
	pipelineName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	scheduleName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

// Validate validates typed checkout declarations for seal-point consumers.
func Validate(cfg Config) error {
	if cfg.Defaults != nil {
		if err := userconfig.ValidateRunRouting("defaults.run", cfg.Defaults.Run); err != nil {
			return err
		}
		if err := validateGuards("defaults.guards", cfg.Defaults.Guards); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for i, p := range cfg.Pipelines {
		key := fmt.Sprintf("pipelines[%d]", i)
		if err := userconfig.ValidateRunRouting(key+".run", p.Run); err != nil {
			return err
		}
		if !pipelineName.MatchString(p.Name) {
			return userconfig.Invalid(key+".name", "expected a pipeline name containing letters, numbers, dots, hyphens or underscores")
		}
		if seen[p.Name] {
			return userconfig.Invalid(key+".name", "duplicate pipeline name")
		}
		seen[p.Name] = true
		if err := ValidateSourceFiles(p.SourceFiles); err != nil {
			return userconfig.Invalid(key+".source_files", err.Error())
		}
		if strings.TrimSpace(p.Entrypoint) == "" {
			return userconfig.Invalid(key+".entrypoint", "entrypoint is required")
		}
		if err := validateGuards(key+".guards", p.Guards); err != nil {
			return err
		}
		if p.On != nil {
			if err := validateSchedules(key+".on.schedule", p.On.Schedule); err != nil {
				return err
			}
		}
	}
	for i, s := range cfg.Sparks {
		key := fmt.Sprintf("sparks[%d]", i)
		if strings.TrimSpace(s.Source) == "" {
			return userconfig.Invalid(key+".source", "source is required")
		}
		if strings.TrimSpace(s.Version) == "" {
			return userconfig.Invalid(key+".version", "version is required")
		}
	}
	return nil
}

// ValidateSourceFiles checks literal repository-relative bundle file paths.
func ValidateSourceFiles(paths []string) error {
	for _, name := range paths {
		if !filepath.IsLocal(name) || strings.ContainsAny(name, "\x00\\:") {
			return fmt.Errorf("source files must be literal repository-relative paths outside .git")
		}
		for _, part := range strings.Split(name, "/") {
			if strings.EqualFold(part, ".git") || part == ".." || part == "." || part == "" {
				return fmt.Errorf("source files must be literal repository-relative paths outside .git")
			}
		}
	}
	return nil
}

func validateGuards(key string, g *Guards) error {
	if g == nil {
		return nil
	}
	for _, field := range []struct {
		name   string
		tokens []string
	}{{"when", g.When}, {"unless", g.Unless}} {
		for i, token := range field.tokens {
			if !validGuard(token) {
				return userconfig.Invalid(fmt.Sprintf("%s.%s[%d]", key, field.name, i), "expected where:local|controller, arg:key=value or git:branch=name|default")
			}
		}
	}
	return nil
}

func validGuard(token string) bool {
	namespace, value, ok := strings.Cut(token, ":")
	if !ok {
		return false
	}
	switch namespace {
	case "where":
		return value == "local" || value == "controller"
	case "git":
		key, value, ok := strings.Cut(value, "=")
		return ok && key == "branch" && value != ""
	case "arg":
		key, value, ok := strings.Cut(value, "=")
		return ok && key != "" && value != ""
	}
	return false
}

func validateSchedules(key string, entries []Schedule) error {
	seen := map[string]bool{}
	for i, s := range entries {
		base := fmt.Sprintf("%s[%d]", key, i)
		name := "default"
		if s.Name != nil {
			name = *s.Name
			if !scheduleName.MatchString(name) || len(name) > 40 {
				return userconfig.Invalid(base+".name", "must match [a-z0-9][a-z0-9-]* and be at most 40 characters")
			}
		} else if len(entries) > 1 {
			return userconfig.Invalid(base+".name", "multiple schedules require names")
		}
		if seen[name] {
			return userconfig.Invalid(base+".name", "duplicate schedule name")
		}
		seen[name] = true
		if _, err := cronspec.Parse(s.Cron); err != nil {
			return userconfig.Invalid(base+".cron", "expected a five-field cron expression")
		}
		if s.Where != "local" && s.Where != "controller" {
			return userconfig.Invalid(base+".where", "must be local or controller")
		}
		if s.TZ != nil && *s.TZ != "local" {
			if *s.TZ == "" {
				return userconfig.Invalid(base+".tz", "timezone must not be empty")
			}
			if _, err := time.LoadLocation(*s.TZ); err != nil {
				return userconfig.Invalid(base+".tz", "unknown timezone")
			}
		}
		if s.Overlap != nil && *s.Overlap != "skip" && *s.Overlap != "queue" {
			return userconfig.Invalid(base+".overlap", "must be skip or queue")
		}
		if s.CatchUp != nil {
			d, err := time.ParseDuration(*s.CatchUp)
			if err != nil || d < limits.ScheduleCatchUpMin || d > limits.ScheduleCatchUpMax {
				return userconfig.Invalid(base+".catch_up", "must be between 2m and 24h")
			}
		}
		for arg := range s.Args {
			if !scheduleName.MatchString(arg) {
				return userconfig.Invalid(base+".args", "argument keys must match [a-z0-9][a-z0-9-]*")
			}
		}
	}
	return nil
}
