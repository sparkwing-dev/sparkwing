package orchestrator

import (
	"context"
	"encoding/json"
)

const (
	triggerEnvPlanAdmissionKey      = "SPARKWING_PLAN_ADMISSION_KEY"
	triggerEnvPlanAdmissionHolderID = "SPARKWING_PLAN_ADMISSION_HOLDER_ID"
	triggerEnvPlanAdmissions        = "SPARKWING_PLAN_ADMISSIONS"
)

type planAdmission struct {
	Key       string
	HolderID  string
	HolderIDs map[string]string
}

type planAdmissionContextKey struct{}

func withPlanAdmission(ctx context.Context, admission planAdmission) context.Context {
	admission = admission.normalized()
	if len(admission.HolderIDs) == 0 {
		return ctx
	}
	return context.WithValue(ctx, planAdmissionContextKey{}, admission)
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
	admission := planAdmission{}
	if raw := env[triggerEnvPlanAdmissions]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &admission.HolderIDs)
	}
	admission = admission.with(
		env[triggerEnvPlanAdmissionKey],
		env[triggerEnvPlanAdmissionHolderID],
	)
	return admission.normalized()
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
		Key:       key,
		HolderID:  holderID,
		HolderIDs: holders,
	}
}

func planAdmissionTriggerEnv(ctx context.Context) map[string]string {
	admission, ok := planAdmissionFromContext(ctx)
	if !ok {
		return nil
	}
	env := map[string]string{
		triggerEnvPlanAdmissionKey:      admission.Key,
		triggerEnvPlanAdmissionHolderID: admission.HolderID,
	}
	if len(admission.HolderIDs) > 0 {
		if payload, err := json.Marshal(admission.HolderIDs); err == nil {
			env[triggerEnvPlanAdmissions] = string(payload)
		}
	}
	return env
}
