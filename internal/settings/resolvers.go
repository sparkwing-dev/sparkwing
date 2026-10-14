package settings

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// ConfigPath resolves config_path with its Table rule, preserving origin for refusal.
func ConfigPath(in Inputs) (string, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// Home resolves home with its Table rule, preserving origin for refusal.
func Home(in Inputs) (string, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// Profile resolves profile with its Table rule, preserving origin for refusal.
func Profile(in Inputs) (string, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// Args resolves args with its Table rule, preserving origin for refusal.
func Args(in Inputs) (map[string]string, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// Guards resolves guards with its Table rule, preserving origin for refusal.
func Guards(in Inputs) (projectconfig.Guards, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// Requires resolves requires with its Table rule, preserving origin for refusal.
func Requires(in Inputs) ([]string, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// Prefers resolves prefers with its Table rule, preserving origin for refusal.
func Prefers(in Inputs) ([]string, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// Workers resolves workers with its Table rule, preserving origin for refusal.
func Workers(in Inputs) (int, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// Output resolves output with its Table rule, preserving origin for refusal.
func Output(in Inputs) (string, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// AdmissionClass resolves admission_class with its Table rule, preserving origin for refusal.
func AdmissionClass(in Inputs) (string, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// Priority resolves priority with its Table rule, preserving origin for refusal.
func Priority(in Inputs) (int, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// AdmissionPolicy resolves admission with its Table rule, preserving origin for refusal.
func AdmissionPolicy(in Inputs) (userconfig.AdmissionConfig, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// Toolchain resolves toolchain with its Table rule, preserving origin for refusal.
func Toolchain(in Inputs) (string, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// PauseTimeout resolves debug.pause_timeout with its Table rule, preserving origin for refusal.
func PauseTimeout(in Inputs) (time.Duration, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// BoxID resolves machine.box_id with its Table rule, preserving origin for refusal.
func BoxID(in Inputs) (string, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// BinaryCache resolves cache with its Table rule, preserving origin for refusal.
func BinaryCache(in Inputs) (userconfig.CacheConfig, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// SecretsKey resolves secrets_key with its Table rule, preserving origin for refusal.
func SecretsKey(in Inputs) (KeyInput, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// CatchUp resolves schedule.catch_up with its Table rule, preserving origin for refusal.
func CatchUp(in Inputs) (time.Duration, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// Runner resolves runner with its Table rule, preserving origin for refusal.
func Runner(in Inputs) (userconfig.RunnerConfig, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// OwnerExecution resolves owner_execution with its Table rule, preserving origin for refusal.
func OwnerExecution(in Inputs) (time.Duration, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// JobTimeout resolves job.timeout with its Table rule, preserving origin for refusal.
func JobTimeout(in Inputs) (time.Duration, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// ApprovalWait resolves approval.wait with its Table rule, preserving origin for refusal.
func ApprovalWait(in Inputs) (time.Duration, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// Retry resolves job.retry with its Table rule, preserving origin for refusal.
func Retry(in Inputs) (int, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// MemoTTL resolves job.memo_ttl with its Table rule, preserving origin for refusal.
func MemoTTL(in Inputs) (time.Duration, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// ClaimWait resolves claim_wait with its Table rule, preserving origin for refusal.
func ClaimWait(in Inputs) (time.Duration, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// Retention resolves retention with its Table rule, preserving origin for refusal.
func Retention(in Inputs) (RetentionValues, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// IdlePoll resolves idle_poll with its Table rule, preserving origin for refusal.
func IdlePoll(in Inputs) (time.Duration, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// ControllerConnection resolves connection with its Table rule, preserving origin for refusal.
func ControllerConnection(in Inputs) (userconfig.Connection, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// RepoLocation resolves repo_location with its Table rule, preserving origin for refusal.
func RepoLocation(in Inputs) (string, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// PauseTargets resolves debug.pause with its Table rule, preserving origin for refusal.
func PauseTargets(in Inputs) (PauseSelection, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// Debug resolves debug with its Table rule, preserving origin for refusal.
func Debug(in Inputs) (bool, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// Publish resolves publish with its Table rule, preserving origin for refusal.
func Publish(in Inputs) (string, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// CacheFrom resolves cache_from with its Table rule, preserving origin for refusal.
func CacheFrom(in Inputs) (string, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// ResourceLimits resolves resources with its Table rule, preserving origin for refusal.
func ResourceLimits(in Inputs) (Resources, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// SourceRef resolves source with its Table rule, preserving origin for refusal.
func SourceRef(in Inputs) (string, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// TokenTTL resolves token.ttl with its Table rule, preserving origin for refusal.
func TokenTTL(in Inputs) (time.Duration, Origin, error) {
	panic("unimplemented: c4-surface-config")
}

// SubmitEnvAllow resolves run.submit_env_allow with its Table rule, preserving origin for refusal.
func SubmitEnvAllow(in Inputs) ([]string, Origin, error) {
	panic("unimplemented: c4-surface-config")
}
