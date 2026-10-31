package runstate

// Event kinds a run's stream carries outside the job and run state rules. EventSecretPreflightRefused records the
// plan a run's required secrets refused, with the scope each is missing from and the command that sets it.
const (
	EventDebugPaused            = "debug.paused"
	EventDebugResumed           = "debug.resumed"
	EventSecretPreflightRefused = "secret_preflight_refused"
)

// RunEventKinds are the registered state and control event kinds of a run's stream: the store refuses a state event
// outside it, and the console's run stream subscribes to exactly this list through web/src/lib/runEventKinds.json.
// Generic kinds appended through Tenant.AppendEvent, such as a note, are not registered and stay unsubscribed.
var RunEventKinds = []string{
	"run_started", "run_cancel_requested", "run_requeued", "run_pending", "run_success", "run_dry_run", "run_failed", "run_cancelled",
	"job_attempt_imported", "job_ready", "job_requeued", "job_revoked", "job_claimed", "job_approval_pending",
	"job_approval_ready", "job_expansion_ready", "job_started", "job_retrying", "job_cancel_requested",
	"job_succeeded", "job_failed", "job_cancelled", "job_skipped", "job_superseded", "job_memoized",
	EventDebugPaused, EventDebugResumed, EventSecretPreflightRefused, "stray_session_reaped",
}
