package settings

import (
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// Profile resolves profile from its permitted sources.
func Profile(in Inputs) (string, Origin, error) {
	return pickRun(in, "profile", func(v runspec.Options) runspec.Setting[string] { return v.Profile }, "")
}

// Workers resolves workers from its permitted sources.
func Workers(in Inputs) (int, Origin, error) {
	fallback := in.Context.NumCPU

	value, origin, err := pickRun(in, "workers", func(v runspec.Options) runspec.Setting[int] { return v.Workers }, fallback)
	if err != nil {
		return value, origin, err
	}
	if value <= 0 {
		err = invalid("workers", origin, "must be positive")
	}
	if in.Context.Detached && origin.Source == Flag {
		err = invalid("workers", origin, "explicit workers is unavailable for detached execution")
	}
	return value, origin, err
}

// Output resolves output from its permitted sources.
func Output(in Inputs) (string, Origin, error) {
	fallback := "json"
	if in.Context.Terminal {
		fallback = "pretty"
	}
	value, origin, err := pickRun(in, "output", func(v runspec.Options) runspec.Setting[string] { return v.Output }, fallback)
	if err != nil {
		return value, origin, err
	}
	if !slices.Contains([]string{"pretty", "json", "plain"}, value) {
		err = invalid("output", origin, "must be pretty, json or plain")
	}
	return value, origin, err
}

// AdmissionClass resolves admission_class from its permitted sources.
func AdmissionClass(in Inputs) (string, Origin, error) {
	fallback := "normal"
	if in.Context.Origin == runspec.OriginSchedule || in.Context.Origin == runspec.OriginRetry {
		fallback = "batch"
	}

	value, origin, err := pickRun(in, "admission_class", func(v runspec.Options) runspec.Setting[string] { return v.AdmissionClass }, fallback)
	if err != nil {
		return value, origin, err
	}
	if !slices.Contains([]string{"critical", "interactive", "normal", "batch"}, value) {
		err = invalid("admission_class", origin, "unknown admission class")
	}
	if !in.Context.Submitter {
		return value, origin, err
	}
	op, e := operatorCeilings(in)
	if e != nil {
		return value, origin, e
	}
	if op.AllowedAdmissionClasses.Set && !slices.Contains(op.AllowedAdmissionClasses.Value, value) {
		err = invalid("admission_class", origin, "not permitted by operator admission classes")
	}
	return value, origin, err
}

// Priority resolves priority from its permitted sources.
func Priority(in Inputs) (int, Origin, error) {
	fallback := 0

	value, origin, err := pickRun(in, "priority", func(v runspec.Options) runspec.Setting[int] { return v.Priority }, fallback)
	if err != nil {
		return value, origin, err
	}
	if !in.Context.Submitter {
		return value, origin, err
	}
	op, e := operatorCeilings(in)
	if e != nil {
		return value, origin, e
	}
	if op.Priority.Set && value > op.Priority.Value {
		err = above("priority", origin, value, op.Priority.Value, fromOrigin(op.Priority.Origin))
	}
	return value, origin, err
}

// Publish resolves publish from its permitted sources.
func Publish(in Inputs) (string, Origin, error) {
	return pickRun(in, "publish", func(v runspec.Options) runspec.Setting[string] { return v.Publish }, "")
}

// CacheFrom resolves cache_from from its permitted sources.
func CacheFrom(in Inputs) (string, Origin, error) {
	return pickRun(in, "cache_from", func(v runspec.Options) runspec.Setting[string] { return v.CacheFrom }, "")
}

// StartAt resolves start_at from the intake selection.
func StartAt(in Inputs) (string, Origin, error) {
	return pickRun(in, "start_at", func(v runspec.Options) runspec.Setting[string] { return v.StartAt }, "")
}

// StopAt resolves stop_at from the intake selection.
func StopAt(in Inputs) (string, Origin, error) {
	return pickRun(in, "stop_at", func(v runspec.Options) runspec.Setting[string] { return v.StopAt }, "")
}

// Only resolves only from the intake selection.
func Only(in Inputs) (string, Origin, error) {
	return pickRun(in, "only", func(v runspec.Options) runspec.Setting[string] { return v.Only }, "")
}

// DryRun resolves dry_run from its permitted sources.
func DryRun(in Inputs) (bool, Origin, error) {
	return pickRun(in, "dry_run", func(v runspec.Options) runspec.Setting[bool] { return v.DryRun }, false)
}

// NoMemo resolves no_memo from its permitted sources.
func NoMemo(in Inputs) (bool, Origin, error) {
	return pickRun(in, "no_memo", func(v runspec.Options) runspec.Setting[bool] { return v.NoMemo }, false)
}

// Debug resolves debug from its permitted sources.
func Debug(in Inputs) (bool, Origin, error) {
	return pickRun(in, "debug", func(v runspec.Options) runspec.Setting[bool] { return v.Debug }, false)
}

// Retry resolves job.retry from its permitted sources.
func Retry(in Inputs) (int, Origin, error) {
	fallback := 0

	value, origin, err := pickRun(in, "job.retry", func(v runspec.Options) runspec.Setting[int] { return v.JobDefaults.Retry }, fallback)
	if err != nil {
		return value, origin, err
	}
	if value < 0 || value > limits.MaxRetries {
		err = invalid("job.retry", origin, "must be between 0 and 10")
	}
	return value, origin, err
}

// Requires preserves an explicit empty list.
func Requires(in Inputs) ([]string, Origin, error) {
	value, origin, err := pickRunList(in, "requires", func(v runspec.Options) runspec.Setting[[]string] { return v.Requires })
	if err == nil {
		value = nodeproto.NormalizeLabels(value)
		if value == nil {
			value = []string{}
		}
		err = validNames("requires", value, origin)
	}
	return value, origin, err
}

// Prefers preserves an explicit empty list.
func Prefers(in Inputs) ([]string, Origin, error) {
	value, origin, err := pickRunList(in, "prefers", func(v runspec.Options) runspec.Setting[[]string] { return v.Prefers })
	if err == nil {
		value = nodeproto.NormalizeLabels(value)
		if value == nil {
			value = []string{}
		}
		err = validNames("prefers", value, origin)
	}
	return value, origin, err
}

// AllowRisk preserves an explicit empty list.
func AllowRisk(in Inputs) ([]string, Origin, error) {
	value, origin, err := pickRunList(in, "allow_risk", func(v runspec.Options) runspec.Setting[[]string] { return v.AllowRisk })
	if err == nil {
		err = validNames("allow_risk", value, origin)
	}
	return value, origin, err
}

// SubmitEnvAllow preserves an explicit empty list.
func SubmitEnvAllow(in Inputs) ([]string, Origin, error) {
	value, origin, err := pickRunList(in, "run.submit_env_allow", func(v runspec.Options) runspec.Setting[[]string] { return v.SubmitEnvAllow })
	if err == nil {
		if validation := procenv.ValidateSubmitEnvAllow(value); validation != nil {
			err = invalid("run.submit_env_allow", origin, "use literal environment names or terminal _* prefixes")
		}
	}
	return value, origin, err
}

// PauseTimeout validates its independent duration bound.
func PauseTimeout(in Inputs) (time.Duration, Origin, error) {
	value, origin, err := pickRun(in, "debug.pause_timeout", func(v runspec.Options) runspec.Setting[time.Duration] { return v.PauseTimeout }, limits.DebugPause)
	if err == nil && value < 1 {
		err = invalid("debug.pause_timeout", origin, "duration is below the permitted minimum")
	}
	return value, origin, err
}

// MemoTTL validates its independent duration bound.
func MemoTTL(in Inputs) (time.Duration, Origin, error) {
	value, origin, err := pickRun(in, "job.memo_ttl", func(v runspec.Options) runspec.Setting[time.Duration] { return v.JobDefaults.MemoTTL }, limits.MemoDefaultTTL)
	if err == nil && value < 1 {
		err = invalid("job.memo_ttl", origin, "duration is below the permitted minimum")
	}
	if err == nil && value > limits.MemoMaxTTL {
		err = above("job.memo_ttl", origin, value, limits.MemoMaxTTL, Origin{Source: Default, Key: "job.memo_ttl"})
	}
	return value, origin, err
}

// ClaimWait validates its independent duration bound.
func ClaimWait(in Inputs) (time.Duration, Origin, error) {
	value, origin, err := pickRun(in, "claim_wait", func(v runspec.Options) runspec.Setting[time.Duration] { return v.ClaimWait }, limits.ClaimWait)
	if err == nil && value < 1 {
		err = invalid("claim_wait", origin, "duration is below the permitted minimum")
	}
	if !in.Context.Submitter {
		return value, origin, err
	}
	op, e := operatorCeilings(in)
	if e != nil {
		return value, origin, e
	}
	if err == nil && op.ClaimWait.Set && value > op.ClaimWait.Value {
		err = above("claim_wait", origin, value, op.ClaimWait.Value, fromOrigin(op.ClaimWait.Origin))
	}
	return value, origin, err
}

// ApprovalWait validates its independent duration bound.
func ApprovalWait(in Inputs) (time.Duration, Origin, error) {
	value, origin, err := pickRun(in, "approval.wait", func(v runspec.Options) runspec.Setting[time.Duration] { return v.JobDefaults.ApprovalWait }, time.Duration(0))
	if err == nil && value < 0 {
		err = invalid("approval.wait", origin, "duration is below the permitted minimum")
	}
	return value, origin, err
}

// NoProgressTimeout validates its independent duration bound.
func NoProgressTimeout(in Inputs) (time.Duration, Origin, error) {
	value, origin, err := pickRun(in, "job.no_progress_timeout", func(v runspec.Options) runspec.Setting[time.Duration] { return v.JobDefaults.NoProgressTimeout }, time.Duration(0))
	if err == nil && value < 0 {
		err = invalid("job.no_progress_timeout", origin, "duration is below the permitted minimum")
	}
	return value, origin, err
}

// RetryBackoff validates its independent duration bound.
func RetryBackoff(in Inputs) (time.Duration, Origin, error) {
	value, origin, err := pickRun(in, "job.retry_backoff", func(v runspec.Options) runspec.Setting[time.Duration] { return v.JobDefaults.RetryBackoff }, time.Duration(0))
	if err == nil && (value < 0 || value > limits.RetryBackoff) {
		err = invalid("job.retry_backoff", origin, "duration is outside the permitted bounds")
	}
	return value, origin, err
}

// Args merges each key independently, including schedule overrides at intake.
func Args(in Inputs) (map[string]string, Origin, error) { return mergeArgs(in) }

// Guards replaces the entire predicate block, including an empty block.
func Guards(in Inputs) (projectconfig.Guards, Origin, error) {
	value, origin, err := pick(in, "guards", func(v Values) *projectconfig.Guards { return v.Guards }, projectconfig.Guards{})
	if err == nil {
		err = projectconfig.Validate(projectconfig.Config{Defaults: &projectconfig.Defaults{Guards: &value}})
	}
	if err != nil {
		return value, origin, invalid("guards", origin, "invalid guard declarations")
	}
	value.When = slices.Clone(value.When)
	value.Unless = slices.Clone(value.Unless)
	return value, origin, nil
}

// RunnerSelection validates routing without granting pool authority.
func RunnerSelection(in Inputs) (runspec.RunnerSelection, Origin, error) {
	value, origin, err := pickRun(in, "runner", func(v runspec.Options) runspec.Setting[runspec.RunnerSelection] { return v.Runner }, runspec.RunnerSelection{Kind: runspec.RunnerAny})
	switch value.Kind {
	case runspec.RunnerAny, runspec.RunnerHere, runspec.RunnerActions:
		if value.Value != "" {
			err = invalid("runner", origin, "this selector takes no value")
		}
	case runspec.RunnerName, runspec.RunnerLabel:
		if strings.TrimSpace(value.Value) == "" {
			err = invalid("runner", origin, "this selector requires a value")
		}
	default:
		err = invalid("runner", origin, "unknown runner selector")
	}
	return value, origin, err
}

// OwnerExecution leaves local execution unbounded and applies the remote operator ceiling.
func OwnerExecution(in Inputs) (time.Duration, Origin, error) {
	if !in.Context.Submitter {
		return pickRun(in, "owner_execution", func(v runspec.Options) runspec.Setting[time.Duration] { return v.OwnerExecutionTimeout }, time.Duration(0))
	}
	if in.Context.Local {
		return 0, Origin{Source: Default, Key: "owner_execution"}, nil
	}
	op, err := operatorCeilings(in)
	if err != nil {
		return 0, Origin{}, err
	}
	ceiling := op.OwnerExecutionTimeout
	if !ceiling.Set {
		ceiling = declaration(limits.RunDeadline, runspec.SettingOrigin{Layer: runspec.LayerDefault, Key: "operator.owner_execution"})
	}
	value, origin, err := pickRun(in, "owner_execution", func(v runspec.Options) runspec.Setting[time.Duration] { return v.OwnerExecutionTimeout }, ceiling.Value)
	if origin.Source == Default {
		value = ceiling.Value
		origin = fromOrigin(ceiling.Origin)
	}
	if err == nil && value <= 0 {
		err = invalid("owner_execution", origin, "remote owner duration must be positive")
	}
	if err == nil && value > ceiling.Value {
		err = above("owner_execution", origin, value, ceiling.Value, fromOrigin(ceiling.Origin))
	}
	return value, origin, err
}

// JobTimeout refuses an author bound above the owner's independent execution ceiling.
func JobTimeout(in Inputs) (time.Duration, Origin, error) {
	value, origin, err := pickRun(in, "job.timeout", func(v runspec.Options) runspec.Setting[time.Duration] { return v.JobDefaults.Timeout }, time.Duration(0))
	if err != nil {
		return value, origin, err
	}
	if origin.Source != Default && value <= 0 {
		return value, origin, invalid("job.timeout", origin, "explicit timeout must be positive")
	}
	if !in.Context.Submitter {
		return value, origin, nil
	}
	owner, from, err := OwnerExecution(in)
	if err != nil {
		return value, origin, err
	}
	if value == 0 {
		return owner, origin, nil
	}
	if owner > 0 && value > owner {
		err = above("job.timeout", origin, value, owner, from)
	}
	return value, origin, err
}
