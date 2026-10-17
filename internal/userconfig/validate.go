package userconfig

import (
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// Decode validates the complete machine schema without filling defaults.
func Decode(path string, raw []byte) (Config, error) {
	var cfg Config
	doc, _, err := parseDoc(path, raw)
	if err != nil {
		return Config{}, err
	}
	if err := doc.Decode(&cfg); err != nil {
		return Config{}, LocateError(path, doc, err)
	}
	return cfg, nil
}

// ValidateConfig validates typed machine settings without echoing credentials.
func ValidateConfig(cfg Config) error {
	for name, p := range cfg.Profiles {
		if strings.TrimSpace(name) == "" {
			return Invalid("profiles", "profile name must not be empty")
		}
		if !validURL(p.Controller.URL) {
			return Invalid("profiles."+name+".controller.url", "expected an HTTP or HTTPS controller URL without credentials")
		}
	}
	if cfg.Runner != nil {
		if err := validateRunner(*cfg.Runner); err != nil {
			return err
		}
	}
	if cfg.Admission != nil {
		if err := validateAdmission(*cfg.Admission); err != nil {
			return err
		}
	}
	if cfg.Cache != nil {
		if cfg.Cache.MaxEntries != nil && *cfg.Cache.MaxEntries < 0 {
			return Invalid("cache.max_entries", "must be non-negative")
		}
		if cfg.Cache.MaxBytes != nil {
			if _, err := ParseByteSize(*cfg.Cache.MaxBytes); err != nil {
				return Invalid("cache.max_bytes", "expected a non-negative byte size")
			}
		}
	}
	if cfg.Debug != nil {
		if err := durationField("debug.pause_timeout", cfg.Debug.PauseTimeout, false); err != nil {
			return err
		}
	}
	if cfg.Machine != nil && cfg.Machine.BoxID != nil && strings.TrimSpace(*cfg.Machine.BoxID) == "" {
		return Invalid("machine.box_id", "must not be empty")
	}
	if cfg.Repos != nil {
		for _, repo := range cfg.Repos.Repos {
			if strings.TrimSpace(repo.Path) == "" {
				return Invalid("repos.repos", "repository path must not be empty")
			}
		}
		for _, path := range cfg.Repos.FallbackPaths {
			if strings.TrimSpace(path) == "" {
				return Invalid("repos.fallback_paths", "path must not be empty")
			}
		}
	}
	if cfg.Run != nil {
		if err := ValidateRunRouting("run.routing", cfg.Run.Routing); err != nil {
			return err
		}
		if err := procenv.ValidateSubmitEnvAllow(cfg.Run.SubmitEnvAllow); err != nil {
			return Invalid("run.submit_env_allow", "use literal environment names or terminal _* prefixes")
		}
	}
	return nil
}

func validURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && u.Fragment == ""
}

func validateRunner(r RunnerConfig) error {
	if r.Controller != nil && !validURL(*r.Controller) {
		return Invalid("runner.controller", "expected an HTTP or HTTPS controller URL without credentials")
	}
	allow, err := sourceurl.ParseRepoAllowlist(r.Repos)
	if err != nil {
		return Invalid("runner.repos", "invalid repository allow-list")
	}
	if r.AcceptWorkingTree != nil && *r.AcceptWorkingTree && allow.Empty() {
		return Invalid("runner.accept_working_tree", "requires a repository allow-list")
	}
	if r.MaxConcurrent != nil && *r.MaxConcurrent < 1 {
		return Invalid("runner.max_concurrent", "must be positive")
	}
	if r.SpawnPolicy != nil && *r.SpawnPolicy != "return-to-queue" {
		return Invalid("runner.spawn_policy", "must be return-to-queue")
	}
	for _, field := range []struct {
		key   string
		value *string
	}{{"poll", r.Poll}, {"lease", r.Lease}, {"heartbeat", r.Heartbeat}} {
		if err := durationField("runner."+field.key, field.value, false); err != nil {
			return err
		}
	}
	if r.Lease != nil && r.Heartbeat != nil {
		lease, err := time.ParseDuration(*r.Lease)
		if err != nil {
			return Invalid("runner.lease", "expected a positive duration")
		}
		heartbeat, err := time.ParseDuration(*r.Heartbeat)
		if err != nil {
			return Invalid("runner.heartbeat", "expected a positive duration")
		}
		if err := limits.CheckRenewal(heartbeat, lease); err != nil {
			return Invalid("runner.heartbeat", "two renewals must fit strictly before lease expiry")
		}
	}
	for _, field := range []struct {
		key   string
		value *string
	}{{"keep_free", r.KeepFree}, {"contribution", r.Contribution}} {
		if field.value != nil {
			if _, err := ParseBudget(*field.value); err != nil {
				return Invalid("runner."+field.key, "invalid resource budget")
			}
		}
	}
	return nil
}

func durationField(key string, value *string, zero bool) error {
	if value == nil {
		return nil
	}
	d, err := time.ParseDuration(*value)
	if err != nil || d < 0 || (!zero && d == 0) {
		if zero {
			return Invalid(key, "expected a non-negative duration")
		}
		return Invalid(key, "expected a positive duration")
	}
	return nil
}

func validateAdmission(a AdmissionConfig) error {
	if a.Mode != nil {
		switch *a.Mode {
		case "classic", "off", "auto", "jev", "custom":
		default:
			return Invalid("admission.mode", "must be classic, off, auto, jev or custom")
		}
	}
	if a.Budget != nil {
		if _, err := ParseBudget(*a.Budget); err != nil {
			return Invalid("admission.budget", "invalid resource budget")
		}
	}
	if a.Custom != nil {
		c := a.Custom
		for class, d := range c.BackfillDelay {
			if !validClass(class) {
				return Invalid("admission.custom.backfill_delay", "unknown workload class")
			}
			if err := durationField("admission.custom.backfill_delay."+class, &d, true); err != nil {
				return err
			}
		}
		for class, w := range c.ClassWeight {
			if !validClass(class) {
				return Invalid("admission.custom.class_weight", "unknown workload class")
			}
			if w < 0 || w > 1000 {
				return Invalid("admission.custom.class_weight."+class, "must be between 0 and 1000")
			}
		}
		if c.InteractiveBurst != nil {
			b := c.InteractiveBurst
			if b.Cores != nil && !inRange(*b.Cores, 0, 4) {
				return Invalid("admission.custom.interactive_burst.cores", "must be between 0 and 4")
			}
			if b.MinSamples != nil && *b.MinSamples < 1 {
				return Invalid("admission.custom.interactive_burst.min_samples", "must be positive")
			}
			if err := durationField("admission.custom.interactive_burst.max_p99", b.MaxP99, true); err != nil {
				return err
			}
		}
	}
	if a.Jev != nil {
		j := a.Jev
		if err := durationField("admission.jev.timeout", j.Timeout, false); err != nil {
			return err
		}
		if err := durationField("admission.jev.max_backfill", j.MaxBackfill, true); err != nil {
			return err
		}
		for _, field := range []struct {
			key   string
			value *float64
		}{{"min_confidence", j.MinConfidence}, {"min_probability", j.MinProbability}} {
			if field.value != nil && !inRange(*field.value, 0, 1) {
				return Invalid("admission.jev."+field.key, "must be between 0 and 1")
			}
		}
	}
	return nil
}

func validClass(s string) bool {
	switch s {
	case "critical", "interactive", "normal", "batch":
		return true
	}
	return false
}

func inRange(n, min, max float64) bool {
	return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= min && n <= max
}
