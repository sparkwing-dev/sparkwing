package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	triggerEnvPlanAdmissionKey      = "SPARKWING_PLAN_ADMISSION_KEY"
	triggerEnvPlanAdmissionHolderID = "SPARKWING_PLAN_ADMISSION_HOLDER_ID"
	triggerEnvPlanAdmissions        = "SPARKWING_PLAN_ADMISSIONS"
	triggerEnvPlanHostAdmission     = "SPARKWING_PLAN_HOST_ADMISSION"
	triggerEnvPlanHostAdmissionKey  = "SPARKWING_PLAN_HOST_ADMISSION_KEY"
)

type planAdmission struct {
	Key              string
	HolderID         string
	HolderIDs        map[string]string
	HostAdmission    bool
	HostAdmissionKey string
}

type planAdmissionContextKey struct{}

func withPlanAdmission(ctx context.Context, admission planAdmission) context.Context {
	admission = admission.normalized()
	if len(admission.HolderIDs) == 0 {
		return ctx
	}
	ctx = context.WithValue(ctx, planAdmissionContextKey{}, admission)
	return sparkwing.WithCommandEnv(ctx, admission.triggerEnv())
}

func planAdmissionFromContext(ctx context.Context) (planAdmission, bool) {
	admission, ok := ctx.Value(planAdmissionContextKey{}).(planAdmission)
	if !ok {
		return planAdmission{}, false
	}
	admission = admission.normalized()
	return admission, len(admission.HolderIDs) > 0
}

func planAdmissionFromTriggerEnv(env map[string]string) planAdmission {
	if env == nil {
		return planAdmission{}
	}
	admission := planAdmission{
		HostAdmission:    env[triggerEnvPlanHostAdmission] == "1",
		HostAdmissionKey: env[triggerEnvPlanHostAdmissionKey],
	}
	if raw := env[triggerEnvPlanAdmissions]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &admission.HolderIDs)
	}
	admission.Key = env[triggerEnvPlanAdmissionKey]
	admission.HolderID = env[triggerEnvPlanAdmissionHolderID]
	return admission.normalized()
}

func planAdmissionFromEnv() planAdmission {
	return planAdmissionFromTriggerEnv(map[string]string{
		triggerEnvPlanAdmissionKey:      os.Getenv(triggerEnvPlanAdmissionKey),
		triggerEnvPlanAdmissionHolderID: os.Getenv(triggerEnvPlanAdmissionHolderID),
		triggerEnvPlanAdmissions:        os.Getenv(triggerEnvPlanAdmissions),
		triggerEnvPlanHostAdmission:     os.Getenv(triggerEnvPlanHostAdmission),
		triggerEnvPlanHostAdmissionKey:  os.Getenv(triggerEnvPlanHostAdmissionKey),
	})
}

func (admission planAdmission) normalized() planAdmission {
	if len(admission.HolderIDs) > 0 {
		holders := make(map[string]string, len(admission.HolderIDs)+1)
		for key, holderID := range admission.HolderIDs {
			if key != "" && holderID != "" {
				holders[key] = holderID
			}
		}
		admission.HolderIDs = holders
	}
	if admission.Key != "" && admission.HolderID != "" {
		if admission.HolderIDs == nil {
			admission.HolderIDs = map[string]string{}
		}
		admission.HolderIDs[admission.Key] = admission.HolderID
	}
	if admission.Key == "" || admission.HolderID == "" {
		for key, holderID := range admission.HolderIDs {
			admission.Key = key
			admission.HolderID = holderID
			break
		}
	}
	if admission.HostAdmission && admission.HostAdmissionKey == "" && len(admission.HolderIDs) == 1 {
		admission.HostAdmissionKey = admission.Key
	}
	if admission.HostAdmissionKey != "" {
		if _, ok := admission.HolderIDs[admission.HostAdmissionKey]; ok {
			admission.HostAdmission = true
		} else {
			admission.HostAdmissionKey = ""
			admission.HostAdmission = false
		}
	}
	return admission
}

func (admission planAdmission) holderFor(key string) (string, bool) {
	admission = admission.normalized()
	holderID, ok := admission.HolderIDs[key]
	return holderID, ok
}

func (admission planAdmission) with(key, holderID string) planAdmission {
	if key == "" || holderID == "" {
		return admission.normalized()
	}
	admission = admission.normalized()
	holders := make(map[string]string, len(admission.HolderIDs)+1)
	for existingKey, existingHolderID := range admission.HolderIDs {
		holders[existingKey] = existingHolderID
	}
	holders[key] = holderID
	return planAdmission{
		Key:              key,
		HolderID:         holderID,
		HolderIDs:        holders,
		HostAdmission:    admission.HostAdmission,
		HostAdmissionKey: admission.HostAdmissionKey,
	}
}

func (admission planAdmission) withHostAdmission(key string) (planAdmission, error) {
	admission = admission.normalized()
	if admission.HostAdmissionKey != "" && admission.HostAdmissionKey != key {
		return planAdmission{}, fmt.Errorf("plan admission already has host-admission key %q; cannot add %q", admission.HostAdmissionKey, key)
	}
	admission.HostAdmission = true
	admission.HostAdmissionKey = key
	return admission.normalized(), nil
}

func (admission planAdmission) hasHostAdmission() bool {
	admission = admission.normalized()
	return admission.HostAdmission && admission.HostAdmissionKey != ""
}

func planAdmissionTriggerEnv(ctx context.Context) map[string]string {
	admission, ok := planAdmissionFromContext(ctx)
	if !ok {
		return nil
	}
	return admission.triggerEnv()
}

func (admission planAdmission) triggerEnv() map[string]string {
	admission = admission.normalized()
	if len(admission.HolderIDs) == 0 {
		return nil
	}
	env := map[string]string{
		triggerEnvPlanAdmissionKey:      admission.Key,
		triggerEnvPlanAdmissionHolderID: admission.HolderID,
	}
	if admission.HostAdmission {
		env[triggerEnvPlanHostAdmission] = "1"
		env[triggerEnvPlanHostAdmissionKey] = admission.HostAdmissionKey
	}
	if len(admission.HolderIDs) > 0 {
		if payload, err := json.Marshal(admission.HolderIDs); err == nil {
			env[triggerEnvPlanAdmissions] = string(payload)
		}
	}
	return env
}
