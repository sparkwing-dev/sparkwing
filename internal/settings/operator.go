package settings

import (
	"math"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func operatorPick[T any](in OperatorInputs, key string, field func(OperatorValues) *T, fallback *T) (*T, Origin) {
	for _, layer := range []struct {
		value  OperatorValues
		source Source
	}{{in.Flags, Flag}, {in.Team, TeamDB}, {in.Global, GlobalDB}, {in.Profile, LimitsProfile}} {
		if v := field(layer.value); v != nil {
			copy := *v
			origin := layer.value.Origins[key]
			origin.Source = layer.source
			if origin.Key == "" {
				origin.Key = key
			}
			return &copy, origin
		}
	}
	return fallback, Origin{Source: Default, Key: key}
}

func pointer[T any](value T) *T { return &value }

// ControllerCeilings resolves each operator key with OperatorPrecedence. Shared
// store quota reservations enforce deployment guards; replica guards only shed.
func ControllerCeilings(in OperatorInputs) (OperatorValues, error) {
	out := OperatorValues{Origins: map[string]Origin{}}
	var origin Origin
	out.RunRetentionDays, origin = operatorPick(in, "operator.run_retention_days", func(v OperatorValues) *int { return v.RunRetentionDays }, pointer(0))
	out.Origins["operator.run_retention_days"] = origin
	if out.RunRetentionDays != nil && (*out.RunRetentionDays < 0) {
		return OperatorValues{}, invalid("operator.run_retention_days", origin, "invalid operator bound")
	}
	out.MetricRetentionDays, origin = operatorPick(in, "operator.node_metric_retention_days", func(v OperatorValues) *int { return v.MetricRetentionDays }, pointer(0))
	out.Origins["operator.node_metric_retention_days"] = origin
	if out.MetricRetentionDays != nil && (*out.MetricRetentionDays < 0) {
		return OperatorValues{}, invalid("operator.node_metric_retention_days", origin, "invalid operator bound")
	}
	out.OwnerExecution, origin = operatorPick(in, "operator.owner_execution", func(v OperatorValues) *time.Duration { return v.OwnerExecution }, pointer(limits.RunDeadline))
	out.Origins["operator.owner_execution"] = origin
	if out.OwnerExecution != nil && (*out.OwnerExecution <= 0) {
		return OperatorValues{}, invalid("operator.owner_execution", origin, "invalid operator bound")
	}
	out.MaxCores, origin = operatorPick(in, "operator.max_cores", func(v OperatorValues) *float64 { return v.MaxCores }, nil)
	out.Origins["operator.max_cores"] = origin
	if out.MaxCores != nil && (math.IsNaN(*out.MaxCores) || math.IsInf(*out.MaxCores, 0) || *out.MaxCores <= 0) {
		return OperatorValues{}, invalid("operator.max_cores", origin, "invalid operator bound")
	}
	out.MaxMemoryBytes, origin = operatorPick(in, "operator.max_memory_bytes", func(v OperatorValues) *int64 { return v.MaxMemoryBytes }, nil)
	out.Origins["operator.max_memory_bytes"] = origin
	if out.MaxMemoryBytes != nil && (*out.MaxMemoryBytes <= 0) {
		return OperatorValues{}, invalid("operator.max_memory_bytes", origin, "invalid operator bound")
	}
	out.MaxPriority, origin = operatorPick(in, "operator.max_priority", func(v OperatorValues) *int { return v.MaxPriority }, nil)
	out.Origins["operator.max_priority"] = origin
	listAllowedAdmissionClasses, origin := operatorPick(in, "operator.allowed_admission_classes", func(v OperatorValues) *[]string {
		if v.AllowedAdmissionClasses == nil {
			return nil
		}
		return &v.AllowedAdmissionClasses
	}, nil)
	out.Origins["operator.allowed_admission_classes"] = origin
	if listAllowedAdmissionClasses != nil {
		out.AllowedAdmissionClasses = slices.Clone(*listAllowedAdmissionClasses)
	}
	listAllowedSources, origin := operatorPick(in, "operator.allowed_sources", func(v OperatorValues) *[]nodeproto.SourceSpec {
		if v.AllowedSources == nil {
			return nil
		}
		return &v.AllowedSources
	}, nil)
	out.Origins["operator.allowed_sources"] = origin
	if listAllowedSources != nil {
		out.AllowedSources = slices.Clone(*listAllowedSources)
	}
	out.MaxClaimWait, origin = operatorPick(in, "operator.max_claim_wait", func(v OperatorValues) *time.Duration { return v.MaxClaimWait }, nil)
	out.Origins["operator.max_claim_wait"] = origin
	if out.MaxClaimWait != nil && (*out.MaxClaimWait <= 0) {
		return OperatorValues{}, invalid("operator.max_claim_wait", origin, "invalid operator bound")
	}
	out.MaxConcurrentRunners, origin = operatorPick(in, "operator.max_concurrent_runners", func(v OperatorValues) *int { return v.MaxConcurrentRunners }, pointer(0))
	out.Origins["operator.max_concurrent_runners"] = origin
	if out.MaxConcurrentRunners != nil && (*out.MaxConcurrentRunners < 0) {
		return OperatorValues{}, invalid("operator.max_concurrent_runners", origin, "invalid operator bound")
	}
	out.MaxGlobalRunners, origin = operatorPick(in, "operator.max_global_runners", func(v OperatorValues) *int { return v.MaxGlobalRunners }, pointer(0))
	out.Origins["operator.max_global_runners"] = origin
	if out.MaxGlobalRunners != nil && (*out.MaxGlobalRunners < 0) {
		return OperatorValues{}, invalid("operator.max_global_runners", origin, "invalid operator bound")
	}
	out.RunnerAlarm, origin = operatorPick(in, "operator.runner_alarm", func(v OperatorValues) *int { return v.RunnerAlarm }, pointer(0))
	out.Origins["operator.runner_alarm"] = origin
	if out.RunnerAlarm != nil && (*out.RunnerAlarm < 0) {
		return OperatorValues{}, invalid("operator.runner_alarm", origin, "invalid operator bound")
	}
	out.MaxJobsPerRun, origin = operatorPick(in, "operator.max_jobs_per_run", func(v OperatorValues) *int { return v.MaxJobsPerRun }, pointer(0))
	out.Origins["operator.max_jobs_per_run"] = origin
	if out.MaxJobsPerRun != nil && (*out.MaxJobsPerRun < 0) {
		return OperatorValues{}, invalid("operator.max_jobs_per_run", origin, "invalid operator bound")
	}
	out.MaxGlobalJobsPerRun, origin = operatorPick(in, "operator.max_global_jobs_per_run", func(v OperatorValues) *int { return v.MaxGlobalJobsPerRun }, pointer(0))
	out.Origins["operator.max_global_jobs_per_run"] = origin
	if out.MaxGlobalJobsPerRun != nil && (*out.MaxGlobalJobsPerRun < 0) {
		return OperatorValues{}, invalid("operator.max_global_jobs_per_run", origin, "invalid operator bound")
	}
	out.MaxRunsPerPrincipalHour, origin = operatorPick(in, "operator.max_runs_per_principal_hour", func(v OperatorValues) *int { return v.MaxRunsPerPrincipalHour }, pointer(0))
	out.Origins["operator.max_runs_per_principal_hour"] = origin
	if out.MaxRunsPerPrincipalHour != nil && (*out.MaxRunsPerPrincipalHour < 0) {
		return OperatorValues{}, invalid("operator.max_runs_per_principal_hour", origin, "invalid operator bound")
	}
	out.MaxGlobalRunsPerHour, origin = operatorPick(in, "operator.max_global_runs_per_hour", func(v OperatorValues) *int { return v.MaxGlobalRunsPerHour }, pointer(0))
	out.Origins["operator.max_global_runs_per_hour"] = origin
	if out.MaxGlobalRunsPerHour != nil && (*out.MaxGlobalRunsPerHour < 0) {
		return OperatorValues{}, invalid("operator.max_global_runs_per_hour", origin, "invalid operator bound")
	}
	out.MinScheduleInterval, origin = operatorPick(in, "operator.min_schedule_interval", func(v OperatorValues) *time.Duration { return v.MinScheduleInterval }, pointer(time.Duration(0)))
	out.Origins["operator.min_schedule_interval"] = origin
	if out.MinScheduleInterval != nil && (*out.MinScheduleInterval < 0) {
		return OperatorValues{}, invalid("operator.min_schedule_interval", origin, "invalid operator bound")
	}
	out.ClaimsPerRunnerMinute, origin = operatorPick(in, "operator.claims_per_runner_minute", func(v OperatorValues) *int { return v.ClaimsPerRunnerMinute }, pointer(0))
	out.Origins["operator.claims_per_runner_minute"] = origin
	if out.ClaimsPerRunnerMinute != nil && (*out.ClaimsPerRunnerMinute < 0) {
		return OperatorValues{}, invalid("operator.claims_per_runner_minute", origin, "invalid operator bound")
	}
	out.HeartbeatsPerRunnerMinute, origin = operatorPick(in, "operator.heartbeats_per_runner_minute", func(v OperatorValues) *int { return v.HeartbeatsPerRunnerMinute }, pointer(0))
	out.Origins["operator.heartbeats_per_runner_minute"] = origin
	if out.HeartbeatsPerRunnerMinute != nil && (*out.HeartbeatsPerRunnerMinute < 0) {
		return OperatorValues{}, invalid("operator.heartbeats_per_runner_minute", origin, "invalid operator bound")
	}
	out.RequestsPerTokenMinute, origin = operatorPick(in, "operator.requests_per_token_minute", func(v OperatorValues) *int { return v.RequestsPerTokenMinute }, pointer(0))
	out.Origins["operator.requests_per_token_minute"] = origin
	if out.RequestsPerTokenMinute != nil && (*out.RequestsPerTokenMinute < 0) {
		return OperatorValues{}, invalid("operator.requests_per_token_minute", origin, "invalid operator bound")
	}
	out.RequestsPerMinuteAlarm, origin = operatorPick(in, "operator.requests_per_minute_alarm", func(v OperatorValues) *int { return v.RequestsPerMinuteAlarm }, pointer(0))
	out.Origins["operator.requests_per_minute_alarm"] = origin
	if out.RequestsPerMinuteAlarm != nil && (*out.RequestsPerMinuteAlarm < 0) {
		return OperatorValues{}, invalid("operator.requests_per_minute_alarm", origin, "invalid operator bound")
	}
	out.MaxLogStreamsPerPrincipal, origin = operatorPick(in, "operator.max_log_streams_per_principal", func(v OperatorValues) *int { return v.MaxLogStreamsPerPrincipal }, pointer(0))
	out.Origins["operator.max_log_streams_per_principal"] = origin
	if out.MaxLogStreamsPerPrincipal != nil && (*out.MaxLogStreamsPerPrincipal < 0) {
		return OperatorValues{}, invalid("operator.max_log_streams_per_principal", origin, "invalid operator bound")
	}
	out.MaxDownloadsPerPrincipal, origin = operatorPick(in, "operator.max_downloads_per_principal", func(v OperatorValues) *int { return v.MaxDownloadsPerPrincipal }, pointer(0))
	out.Origins["operator.max_downloads_per_principal"] = origin
	if out.MaxDownloadsPerPrincipal != nil && (*out.MaxDownloadsPerPrincipal < 0) {
		return OperatorValues{}, invalid("operator.max_downloads_per_principal", origin, "invalid operator bound")
	}
	out.ShedQueueDepth, origin = operatorPick(in, "operator.shed_queue_depth", func(v OperatorValues) *int { return v.ShedQueueDepth }, pointer(0))
	out.Origins["operator.shed_queue_depth"] = origin
	if out.ShedQueueDepth != nil && (*out.ShedQueueDepth < 0) {
		return OperatorValues{}, invalid("operator.shed_queue_depth", origin, "invalid operator bound")
	}
	out.EgressDailyAlarmBytes, origin = operatorPick(in, "operator.egress_daily_alarm_bytes", func(v OperatorValues) *int64 { return v.EgressDailyAlarmBytes }, pointer(int64(0)))
	out.Origins["operator.egress_daily_alarm_bytes"] = origin
	if out.EgressDailyAlarmBytes != nil && (*out.EgressDailyAlarmBytes < 0) {
		return OperatorValues{}, invalid("operator.egress_daily_alarm_bytes", origin, "invalid operator bound")
	}
	out.EnforceIdleClaimPoll, origin = operatorPick(in, "operator.enforce_idle_claim_poll", func(v OperatorValues) *bool { return v.EnforceIdleClaimPoll }, pointer(false))
	out.Origins["operator.enforce_idle_claim_poll"] = origin
	for _, class := range out.AllowedAdmissionClasses {
		if !slices.Contains([]string{"critical", "interactive", "normal", "batch"}, class) {
			return OperatorValues{}, invalid("operator.allowed_admission_classes", out.Origins["operator.allowed_admission_classes"], "unknown admission class")
		}
	}
	for _, policy := range out.AllowedSources {
		if err := validateSource(policy, true); err != nil {
			return OperatorValues{}, invalid("operator.allowed_sources", out.Origins["operator.allowed_sources"], "allowed source must be a complete valid policy")
		}
	}
	return out, nil
}

func operatorCeilings(in Inputs) (runspec.Ceilings, error) {
	if in.ceilings != nil {
		return *in.ceilings, nil
	}
	values, err := ControllerCeilings(in.Operator)
	if err != nil {
		return runspec.Ceilings{}, err
	}
	return ProjectCeilings(values)
}
