package orchestrator

import "context"

const (
	triggerEnvPlanAdmissionKey      = "SPARKWING_PLAN_ADMISSION_KEY"
	triggerEnvPlanAdmissionHolderID = "SPARKWING_PLAN_ADMISSION_HOLDER_ID"
)

type planAdmission struct {
	Key      string
	HolderID string
}

type planAdmissionContextKey struct{}

func withPlanAdmission(ctx context.Context, admission planAdmission) context.Context {
	if admission.Key == "" || admission.HolderID == "" {
		return ctx
	}
	return context.WithValue(ctx, planAdmissionContextKey{}, admission)
}

func planAdmissionFromContext(ctx context.Context) (planAdmission, bool) {
	admission, ok := ctx.Value(planAdmissionContextKey{}).(planAdmission)
	return admission, ok && admission.Key != "" && admission.HolderID != ""
}

func planAdmissionFromTriggerEnv(env map[string]string) planAdmission {
	if env == nil {
		return planAdmission{}
	}
	return planAdmission{
		Key:      env[triggerEnvPlanAdmissionKey],
		HolderID: env[triggerEnvPlanAdmissionHolderID],
	}
}

func planAdmissionTriggerEnv(ctx context.Context) map[string]string {
	admission, ok := planAdmissionFromContext(ctx)
	if !ok {
		return nil
	}
	return map[string]string{
		triggerEnvPlanAdmissionKey:      admission.Key,
		triggerEnvPlanAdmissionHolderID: admission.HolderID,
	}
}
