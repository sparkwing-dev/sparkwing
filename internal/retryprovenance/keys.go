// Package retryprovenance defines private metadata carried between the
// controller and local retry consumer. These keys are persisted in TriggerEnv
// but are never exported into a pipeline process environment.
package retryprovenance

const (
	RepoDirKey  = "_SPARKWING_RETRY_REPO_DIR"
	PlanHashKey = "_SPARKWING_RETRY_PLAN_HASH"
)
