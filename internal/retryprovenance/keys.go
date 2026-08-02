// Package retryprovenance defines private metadata carried between the
// controller and local retry consumer. These keys are persisted in TriggerEnv
// but are never exported into a pipeline process environment.
package retryprovenance

const (
	RepoDirKey      = "_SPARKWING_RETRY_REPO_DIR"
	RepoIdentityKey = "_SPARKWING_RETRY_REPO_URL"
	RevisionKey     = "_SPARKWING_RETRY_REVISION"
	PlanHashKey     = "_SPARKWING_RETRY_PLAN_HASH"

	// RecordedRevisionSnapshotPolicy names the local retry content policy:
	// compile and execute only files materialized from the source run's
	// recorded commit, never mutable working-tree contents.
	RecordedRevisionSnapshotPolicy = "recorded_revision_snapshot"
)
