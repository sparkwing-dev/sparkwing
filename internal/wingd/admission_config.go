package wingd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"go.yaml.in/yaml/v3"
)

const AdmissionConfigFilename = "admission.yaml"

type admissionConfigFile struct {
	Mode   string                 `yaml:"mode"`
	Custom admissionCustomFile    `yaml:"custom"`
	Jev    admissionJevConfigFile `yaml:"jev"`
}

type admissionCustomFile struct {
	BackfillDelay    map[string]string  `yaml:"backfill_delay"`
	ClassWeight      map[string]int     `yaml:"class_weight"`
	AgingEvery       *uint64            `yaml:"aging_every"`
	InteractiveBurst admissionBurstFile `yaml:"interactive_burst"`
}

type admissionBurstFile struct {
	Cores      *float64 `yaml:"cores"`
	MaxP99     string   `yaml:"max_p99"`
	MinSamples *int     `yaml:"min_samples"`
}

type admissionJevConfigFile struct {
	Model          string  `yaml:"model"`
	Endpoint       string  `yaml:"endpoint"`
	Timeout        string  `yaml:"timeout"`
	MinConfidence  float64 `yaml:"min_confidence"`
	MinProbability float64 `yaml:"min_probability"`
	MaxBackfill    string  `yaml:"max_backfill"`
}

// ResolveAdmissionPolicy reads the optional machine-local policy. A missing
// file selects Auto, so admission improves without per-repository setup.
func ResolveAdmissionPolicy(path string) (AdmissionPolicy, string, error) {
	if path == "" {
		var err error
		path, err = fssecure.ConfigFile(AdmissionConfigFilename)
		if err != nil {
			return AdmissionPolicy{}, "", err
		}
	}
	f, err := fssecure.OpenPrivateConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultAdmissionPolicy(), "default", nil
	}
	if err != nil {
		return AdmissionPolicy{}, "", fmt.Errorf("admission policy: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return AdmissionPolicy{}, "", fmt.Errorf("admission policy: read %s: %w", path, err)
	}
	var raw admissionConfigFile
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return AdmissionPolicy{}, "", fmt.Errorf("admission policy: parse %s: %w", path, err)
	}
	policy := DefaultAdmissionPolicy()
	if raw.Mode != "" {
		policy.Mode = admission.Mode(strings.ToLower(strings.TrimSpace(raw.Mode)))
	}
	switch policy.Mode {
	case admission.ModeOff, admission.ModeAuto, admission.ModeJev, admission.ModeCustom:
	default:
		return AdmissionPolicy{}, "", fmt.Errorf("admission policy: mode %q must be off, auto, jev, or custom", raw.Mode)
	}
	for name, value := range raw.Custom.BackfillDelay {
		class := admission.WorkloadClass(strings.ToLower(strings.TrimSpace(name)))
		if admission.NormalizeWorkloadClass(class) != class {
			return AdmissionPolicy{}, "", fmt.Errorf("admission policy: unknown workload class %q", name)
		}
		d, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil || d < 0 {
			return AdmissionPolicy{}, "", fmt.Errorf("admission policy: custom.backfill_delay.%s must be a non-negative duration", name)
		}
		policy.Scheduling.BackfillDelay[class] = d
	}
	for name, weight := range raw.Custom.ClassWeight {
		class := admission.WorkloadClass(strings.ToLower(strings.TrimSpace(name)))
		if admission.NormalizeWorkloadClass(class) != class {
			return AdmissionPolicy{}, "", fmt.Errorf("admission policy: unknown workload class %q", name)
		}
		if weight < 0 || weight > 1000 {
			return AdmissionPolicy{}, "", fmt.Errorf("admission policy: custom.class_weight.%s must be between 0 and 1000", name)
		}
		policy.Scheduling.ClassWeight[class] = weight
	}
	if raw.Custom.AgingEvery != nil {
		policy.Scheduling.AgingEvery = *raw.Custom.AgingEvery
	}
	if err := applyBurstConfig(&policy.Scheduling.Burst, raw.Custom.InteractiveBurst); err != nil {
		return AdmissionPolicy{}, "", err
	}
	if err := applyJevConfig(&policy.Jev, raw.Jev); err != nil {
		return AdmissionPolicy{}, "", err
	}
	return policy, path, nil
}

func applyBurstConfig(dst *admission.BurstPolicy, raw admissionBurstFile) error {
	if raw.Cores != nil {
		if *raw.Cores < 0 || *raw.Cores > 4 {
			return fmt.Errorf("admission policy: custom.interactive_burst.cores must be between 0 and 4")
		}
		dst.MaxCores = *raw.Cores
	}
	if raw.MaxP99 != "" {
		d, err := time.ParseDuration(strings.TrimSpace(raw.MaxP99))
		if err != nil || d < 0 {
			return fmt.Errorf("admission policy: custom.interactive_burst.max_p99 must be a non-negative duration")
		}
		dst.MaxP99 = d
	}
	if raw.MinSamples != nil {
		if *raw.MinSamples < 1 {
			return fmt.Errorf("admission policy: custom.interactive_burst.min_samples must be positive")
		}
		dst.MinSamples = *raw.MinSamples
	}
	return nil
}

func applyJevConfig(dst *JevPolicy, raw admissionJevConfigFile) error {
	dst.Model = strings.TrimSpace(raw.Model)
	dst.Endpoint = strings.TrimSpace(raw.Endpoint)
	if raw.Timeout != "" {
		d, err := time.ParseDuration(strings.TrimSpace(raw.Timeout))
		if err != nil || d <= 0 {
			return fmt.Errorf("admission policy: jev.timeout must be a positive duration")
		}
		dst.Timeout = d
	}
	if raw.MinConfidence != 0 {
		if raw.MinConfidence < 0 || raw.MinConfidence > 1 {
			return fmt.Errorf("admission policy: jev.min_confidence must be between 0 and 1")
		}
		dst.MinConfidence = raw.MinConfidence
	}
	if raw.MinProbability != 0 {
		if raw.MinProbability < 0 || raw.MinProbability > 1 {
			return fmt.Errorf("admission policy: jev.min_probability must be between 0 and 1")
		}
		dst.MinProbability = raw.MinProbability
	}
	if raw.MaxBackfill != "" {
		d, err := time.ParseDuration(strings.TrimSpace(raw.MaxBackfill))
		if err != nil || d < 0 {
			return fmt.Errorf("admission policy: jev.max_backfill must be a non-negative duration")
		}
		dst.MaxBackfill = d
	}
	return nil
}
