package wingd

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

// ResolveAdmissionPolicy reads the shared machine configuration and selects local scheduling policy.
func ResolveAdmissionPolicy() (AdmissionPolicy, string, error) {
	path, err := userconfig.Path()
	if err != nil {
		return AdmissionPolicy{}, "", err
	}
	cfg, err := userconfig.ReadConfig(path)
	if err != nil {
		return AdmissionPolicy{}, "", err
	}
	if cfg.Admission == nil {
		return defaultAdmissionPolicy(), "default", nil
	}
	policy, err := admissionPolicyFrom(*cfg.Admission)
	return policy, path, err
}

func admissionPolicyFrom(raw userconfig.AdmissionConfig) (AdmissionPolicy, error) {
	policy := defaultAdmissionPolicy()
	policy.Mode = admission.Mode(configured(raw.Mode, string(policy.Mode)))
	if policy.Mode == admission.ModeAuto || policy.Mode == admission.ModeCustom {
		policy.Scheduling = admission.AutoPolicy()
	}
	type durationSetting struct {
		value  *string
		target *time.Duration
	}
	var durations []durationSetting
	if custom := raw.Custom; custom != nil && (policy.Mode == admission.ModeCustom) {
		for name, value := range custom.BackfillDelay {
			duration, err := time.ParseDuration(value)
			if err != nil {
				return AdmissionPolicy{}, err
			}
			policy.Scheduling.BackfillDelay[admission.WorkloadClass(name)] = duration
		}
		for name, weight := range custom.ClassWeight {
			policy.Scheduling.ClassWeight[admission.WorkloadClass(name)] = weight
		}
		policy.Scheduling.AgingEvery = configured(custom.AgingEvery, policy.Scheduling.AgingEvery)
		if burst := custom.InteractiveBurst; burst != nil {
			policy.Scheduling.Burst.MaxCores = configured(burst.Cores, policy.Scheduling.Burst.MaxCores)
			policy.Scheduling.Burst.MinSamples = configured(burst.MinSamples, policy.Scheduling.Burst.MinSamples)
			durations = append(durations, durationSetting{burst.MaxP99, &policy.Scheduling.Burst.MaxP99})
		}
	}
	for _, setting := range durations {
		if setting.value != nil {
			duration, err := time.ParseDuration(*setting.value)
			if err != nil {
				return AdmissionPolicy{}, err
			}
			*setting.target = duration
		}
	}
	return policy, nil
}

func configured[T any](value *T, fallback T) T {
	if value != nil {
		return *value
	}
	return fallback
}
