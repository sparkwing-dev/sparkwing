package settings

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// ConfigPath resolves config_path from its permitted sources.
func ConfigPath(in Inputs) (string, Origin, error) {
	fallback := filepath.Join(in.Context.UserHome, ".config", "sparkwing", "config.yaml")
	if in.Context.XDGConfigHome != "" {
		fallback = filepath.Join(in.Context.XDGConfigHome, "sparkwing", "config.yaml")
	}
	value, origin, err := pick(in, "config_path", func(v Values) *string { return v.ConfigPath }, fallback)
	if err != nil {
		return value, origin, err
	}
	if strings.TrimSpace(value) == "" {
		err = invalid("config_path", origin, "must not be empty")
	}
	return value, origin, err
}

// Home resolves home from its permitted sources.
func Home(in Inputs) (string, Origin, error) {
	fallback := filepath.Join(in.Context.UserHome, ".sparkwing")

	value, origin, err := pick(in, "home", func(v Values) *string { return v.Home }, fallback)
	if err != nil {
		return value, origin, err
	}
	if strings.TrimSpace(value) == "" {
		err = invalid("home", origin, "must not be empty")
	}
	return value, origin, err
}

// Profile resolves profile from its permitted sources.
func Profile(in Inputs) (string, Origin, error) {
	fallback := ""

	value, origin, err := pick(in, "profile", func(v Values) *string { return v.Profile }, fallback)
	if err != nil {
		return value, origin, err
	}
	return value, origin, err
}

// Workers resolves workers from its permitted sources.
func Workers(in Inputs) (int, Origin, error) {
	fallback := in.Context.NumCPU

	value, origin, err := pick(in, "workers", func(v Values) *int { return v.Workers }, fallback)
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
	value, origin, err := pick(in, "output", func(v Values) *string { return v.Output }, fallback)
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

	value, origin, err := pick(in, "admission_class", func(v Values) *string { return v.AdmissionClass }, fallback)
	if err != nil {
		return value, origin, err
	}
	if !slices.Contains([]string{"critical", "interactive", "normal", "batch"}, value) {
		err = invalid("admission_class", origin, "unknown admission class")
	}
	if !in.Context.Submitter {
		return value, origin, err
	}
	op, e := ControllerCeilings(in.Operator)
	if e != nil {
		return value, origin, e
	}
	if op.AllowedAdmissionClasses != nil && !slices.Contains(op.AllowedAdmissionClasses, value) {
		err = invalid("admission_class", origin, "not permitted by operator admission classes")
	}
	return value, origin, err
}

// Priority resolves priority from its permitted sources.
func Priority(in Inputs) (int, Origin, error) {
	fallback := 0

	value, origin, err := pick(in, "priority", func(v Values) *int { return v.Priority }, fallback)
	if err != nil {
		return value, origin, err
	}
	if !in.Context.Submitter {
		return value, origin, err
	}
	op, e := ControllerCeilings(in.Operator)
	if e != nil {
		return value, origin, e
	}
	if op.MaxPriority != nil && value > *op.MaxPriority {
		err = above("priority", origin, value, *op.MaxPriority, op.Origins["operator.max_priority"])
	}
	return value, origin, err
}

// Toolchain resolves toolchain from its permitted sources.
func Toolchain(in Inputs) (string, Origin, error) {
	fallback := "auto"

	value, origin, err := pick(in, "toolchain", func(v Values) *string { return v.Toolchain }, fallback)
	if err != nil {
		return value, origin, err
	}
	if value != "auto" && value != "local" {
		err = invalid("toolchain", origin, "must be auto or local")
	}
	return value, origin, err
}

// BoxID resolves machine.box_id from its permitted sources.
func BoxID(in Inputs) (string, Origin, error) {
	fallback := in.Context.Hostname

	value, origin, err := pick(in, "machine.box_id", func(v Values) *string { return v.BoxID }, fallback)
	if err != nil {
		return value, origin, err
	}
	if strings.TrimSpace(value) == "" {
		err = invalid("machine.box_id", origin, "must not be empty")
	}
	return value, origin, err
}

// Publish resolves publish from its permitted sources.
func Publish(in Inputs) (string, Origin, error) {
	fallback := ""

	value, origin, err := pick(in, "publish", func(v Values) *string { return v.Publish }, fallback)
	if err != nil {
		return value, origin, err
	}
	return value, origin, err
}

// CacheFrom resolves cache_from from its permitted sources.
func CacheFrom(in Inputs) (string, Origin, error) {
	fallback := ""

	value, origin, err := pick(in, "cache_from", func(v Values) *string { return v.CacheFrom }, fallback)
	if err != nil {
		return value, origin, err
	}
	return value, origin, err
}

// DryRun resolves dry_run from its permitted sources.
func DryRun(in Inputs) (bool, Origin, error) {
	fallback := false

	value, origin, err := pick(in, "dry_run", func(v Values) *bool { return v.DryRun }, fallback)
	if err != nil {
		return value, origin, err
	}
	return value, origin, err
}

// NoMemo resolves no_memo from its permitted sources.
func NoMemo(in Inputs) (bool, Origin, error) {
	fallback := false

	value, origin, err := pick(in, "no_memo", func(v Values) *bool { return v.NoMemo }, fallback)
	if err != nil {
		return value, origin, err
	}
	return value, origin, err
}

// Debug resolves debug from its permitted sources.
func Debug(in Inputs) (bool, Origin, error) {
	fallback := false

	value, origin, err := pick(in, "debug", func(v Values) *bool { return v.Debug }, fallback)
	if err != nil {
		return value, origin, err
	}
	return value, origin, err
}

// Retry resolves job.retry from its permitted sources.
func Retry(in Inputs) (int, Origin, error) {
	fallback := 0

	value, origin, err := pick(in, "job.retry", func(v Values) *int { return v.Retry }, fallback)
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
	value, origin, err := pickList(in, "requires", func(v Values) []string { return v.Requires })
	if err == nil {
		err = validNames("requires", value, origin)
	}
	return value, origin, err
}

// Prefers preserves an explicit empty list.
func Prefers(in Inputs) ([]string, Origin, error) {
	value, origin, err := pickList(in, "prefers", func(v Values) []string { return v.Prefers })
	if err == nil {
		err = validNames("prefers", value, origin)
	}
	return value, origin, err
}

// AllowRisk preserves an explicit empty list.
func AllowRisk(in Inputs) ([]string, Origin, error) {
	value, origin, err := pickList(in, "allow_risk", func(v Values) []string { return v.AllowRisk })
	if err == nil {
		err = validNames("allow_risk", value, origin)
	}
	return value, origin, err
}

// SubmitEnvAllow preserves an explicit empty list.
func SubmitEnvAllow(in Inputs) ([]string, Origin, error) {
	value, origin, err := pickList(in, "run.submit_env_allow", func(v Values) []string { return v.SubmitEnvAllow })
	if err == nil {
		if validation := procenv.ValidateSubmitEnvAllow(value); validation != nil {
			err = invalid("run.submit_env_allow", origin, "use literal environment names or terminal _* prefixes")
		}
	}
	return value, origin, err
}

// PauseTimeout validates its independent duration bound.
func PauseTimeout(in Inputs) (time.Duration, Origin, error) {
	value, origin, err := pick(in, "debug.pause_timeout", func(v Values) *time.Duration { return v.PauseTimeout }, limits.DebugPause)
	if err == nil && value < 1 {
		err = invalid("debug.pause_timeout", origin, "duration is below the permitted minimum")
	}
	return value, origin, err
}

// CatchUp validates its independent duration bound.
func CatchUp(in Inputs) (time.Duration, Origin, error) {
	value, origin, err := pick(in, "schedule.catch_up", func(v Values) *time.Duration { return v.CatchUp }, limits.ScheduleCatchUpDefault)
	if err == nil && value < limits.ScheduleCatchUpMin {
		err = invalid("schedule.catch_up", origin, "duration is below the permitted minimum")
	}
	if err == nil && value > limits.ScheduleCatchUpMax {
		err = above("schedule.catch_up", origin, value, limits.ScheduleCatchUpMax, Origin{Source: Default, Key: "schedule.catch_up"})
	}
	return value, origin, err
}

// MemoTTL validates its independent duration bound.
func MemoTTL(in Inputs) (time.Duration, Origin, error) {
	value, origin, err := pick(in, "job.memo_ttl", func(v Values) *time.Duration { return v.MemoTTL }, limits.MemoDefaultTTL)
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
	value, origin, err := pick(in, "claim_wait", func(v Values) *time.Duration { return v.ClaimWait }, limits.ClaimWait)
	if err == nil && value < 1 {
		err = invalid("claim_wait", origin, "duration is below the permitted minimum")
	}
	if !in.Context.Submitter {
		return value, origin, err
	}
	op, e := ControllerCeilings(in.Operator)
	if e != nil {
		return value, origin, e
	}
	if err == nil && op.MaxClaimWait != nil && value > *op.MaxClaimWait {
		err = above("claim_wait", origin, value, *op.MaxClaimWait, op.Origins["operator.max_claim_wait"])
	}
	return value, origin, err
}

// ApprovalWait validates its independent duration bound.
func ApprovalWait(in Inputs) (time.Duration, Origin, error) {
	value, origin, err := pick(in, "approval.wait", func(v Values) *time.Duration { return v.ApprovalWait }, time.Duration(0))
	if err == nil && value < 0 {
		err = invalid("approval.wait", origin, "duration is below the permitted minimum")
	}
	return value, origin, err
}

// NoProgressTimeout validates its independent duration bound.
func NoProgressTimeout(in Inputs) (time.Duration, Origin, error) {
	value, origin, err := pick(in, "job.no_progress_timeout", func(v Values) *time.Duration { return v.NoProgressTimeout }, time.Duration(0))
	if err == nil && value < 0 {
		err = invalid("job.no_progress_timeout", origin, "duration is below the permitted minimum")
	}
	return value, origin, err
}

// RetryBackoff validates its independent duration bound.
func RetryBackoff(in Inputs) (time.Duration, Origin, error) {
	value, origin, err := pick(in, "job.retry_backoff", func(v Values) *time.Duration { return v.RetryBackoff }, time.Duration(0))
	if err == nil && value < 0 {
		err = invalid("job.retry_backoff", origin, "duration is below the permitted minimum")
	}
	return value, origin, err
}

// TokenTTL validates its independent duration bound.
func TokenTTL(in Inputs) (time.Duration, Origin, error) {
	value, origin, err := pick(in, "token.ttl", func(v Values) *time.Duration { return v.TokenTTL }, time.Duration(0))
	if err == nil && value < 0 {
		err = invalid("token.ttl", origin, "duration is below the permitted minimum")
	}
	return value, origin, err
}

// IdlePoll validates its independent duration bound.
func IdlePoll(in Inputs) (time.Duration, Origin, error) {
	value, origin, err := pick(in, "idle_poll", func(v Values) *time.Duration { return v.IdlePoll }, limits.ClaimPoll)
	if err == nil && value < 1 {
		err = invalid("idle_poll", origin, "duration is below the permitted minimum")
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
	value, origin, err := pick(in, "runner", func(v Values) *runspec.RunnerSelection { return v.RunnerSelection }, runspec.RunnerSelection{Kind: runspec.RunnerAny})
	switch value.Kind {
	case runspec.RunnerAny, runspec.RunnerHere:
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
		return pick(in, "owner_execution", func(v Values) *time.Duration { return v.OwnerExecution }, time.Duration(0))
	}
	if in.Context.Local {
		return 0, Origin{Source: Default, Key: "owner_execution"}, nil
	}
	op, err := ControllerCeilings(in.Operator)
	if err != nil {
		return 0, Origin{}, err
	}
	value, origin, err := pick(in, "owner_execution", func(v Values) *time.Duration { return v.OwnerExecution }, *op.OwnerExecution)
	if origin.Source == Default {
		origin = op.Origins["operator.owner_execution"]
	}
	if err == nil && value <= 0 {
		err = invalid("owner_execution", origin, "remote owner duration must be positive")
	}
	if err == nil && value > *op.OwnerExecution {
		err = above("owner_execution", origin, value, *op.OwnerExecution, op.Origins["operator.owner_execution"])
	}
	return value, origin, err
}

// JobTimeout refuses an author bound above the owner's independent execution ceiling.
func JobTimeout(in Inputs) (time.Duration, Origin, error) {
	value, origin, err := pick(in, "job.timeout", func(v Values) *time.Duration { return v.JobTimeout }, time.Duration(0))
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

// Checkout combines each policy field without changing acquisition identity.
func Checkout(in Inputs) (nodeproto.SourceSpec, Origin, error) {
	value, _, origin, err := checkout(in)
	return value, origin, err
}
