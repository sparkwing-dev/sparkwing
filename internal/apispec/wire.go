package main

import (
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	wire "github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type wireOperation struct {
	summary    string
	request    reflect.Type
	response   reflect.Type
	parameters []any
	variants   []reflect.Type
	selector   reflect.Type
}

func operation(summary string, request, response any, parameters ...any) wireOperation {
	return wireOperation{summary: summary, request: reflect.TypeOf(request), response: reflect.TypeOf(response), parameters: parameters}
}

func query(name, kind string) schema {
	return schema{"in": "query", "name": name, "schema": schema{"type": kind}}
}

func wireOperations() map[string]wireOperation {
	result := map[string]wireOperation{
		"GET /api/v1/operator/limits":                          operation("Read deployment operational guards", nil, wire.OperatorLimits{}),
		"PUT /api/v1/operator/limits":                          operation("Set deployment operational guards", wire.OperatorLimitsWrite{}, wire.OperatorLimits{}),
		"GET /api/v1/operator/object-store/breaker":            operation("Read object-store request budgets", nil, wire.ObjectStoreBreaker{}),
		"POST /api/v1/operator/object-store/reset-breaker":     operation("Reset object-store request budgets", nil, wire.ObjectStoreBreaker{}),
		"GET /api/v1/capabilities":                             operation("Read engine compatibility", nil, wire.Compatibility{}, query("engine_version", "string")),
		"POST /api/v1/runs":                                    operation("Create a run", wire.CreateRunRequest{}, wire.CreateRunResponse{}),
		"GET /api/v1/runs":                                     operation("List runs", nil, wire.RunPage{}),
		"GET /api/v1/runs/{run}":                               operation("Read public run detail", nil, wire.RunDetail{}),
		"DELETE /api/v1/runs/{run}":                            operation("Delete a run", nil, nil),
		"GET /api/v1/runs/{run}/snapshot":                      operation("Read private scheduling state", nil, wire.ScheduleSnapshot{}),
		"GET /api/v1/runs/{run}/receipt":                       operation("Read a run receipt", nil, wire.Receipt{}),
		"GET /api/v1/runs/{run}/attempts":                      operation("Read public retry history", nil, wire.RunPage{}),
		"GET /api/v1/runs/{run}/steps":                         operation("Read run steps", nil, wire.RunSteps{}),
		"GET /api/v1/runs/{run}/jobs/{job}":                    operation("Read a public job and its execution location", nil, wire.DebugJob{}),
		"GET /api/v1/runs/{run}/jobs/{job}/debug-pause":        operation("Read a public job pause", nil, wire.DebugPauseResult{}),
		"POST /api/v1/runs/{run}/plan":                         operation("Accept a sealed plan", wire.PlanSubmission{}, wire.PlanAccepted{}),
		"POST /api/v1/runs/{run}/finish":                       operation("Finish an owned run", wire.FinishRequest{}, wire.Applied{}),
		"POST /api/v1/runs/{run}/cancel":                       operation("Request run cancellation", wire.CancelRequest{}, wire.Applied{}),
		"POST /api/v1/runs/{run}/retry":                        operation("Retry a sealed run", wire.RetryRequest{}, wire.CreateRunResponse{}),
		"POST /api/v1/claims/{claim}/heartbeat":                operation("Renew a claim", wire.RenewalRequest{}, wire.RenewalResponse{}),
		"POST /api/v1/claims/exchange":                         operation("Exchange a one-use bootstrap", nil, wire.ExchangeResponse{}),
		"POST /api/v1/launcher/sync":                           operation("Reconcile observed launcher jobs", wire.LauncherSyncRequest{}, wire.LauncherSyncResponse{}),
		"POST /api/v1/runs/{run}/jobs/{job}/transition":        operation("Apply a fenced job transition", wire.JobTransitionRequest{}, wire.Applied{}),
		"POST /api/v1/runs/{run}/jobs/{job}/reconcile":         operation("Reconcile persisted job execution", wire.ReconcileRequest{}, wire.ReconcileResponse{}),
		"POST /api/v1/runs/{run}/jobs/{job}/expansion":         operation("Accept a generated expansion", wire.ExpansionSubmission{}, wire.ExpansionAccepted{}),
		"POST /api/v1/runs/{run}/jobs/{job}/cancel":            operation("Request job cancellation", wire.CancelRequest{}, wire.Applied{}),
		"GET /api/v1/runs/{run}/jobs/{job}/failure":            operation("Read an authorized persisted failure", nil, (*nodeproto.JobFailure)(nil), query("generation", "integer")),
		"GET /api/v1/runs/{run}/logs":                          operation("Read persisted run logs", nil, wire.LogPage{}),
		"GET /api/v1/runs/{run}/jobs/{job}/logs":               operation("Read persisted job logs", nil, wire.LogPage{}),
		"GET /api/v1/runs/{run}/logs/search":                   operation("Search persisted run logs", nil, wire.LogPage{}),
		"GET /api/v1/runs/grep":                                operation("Search logs across runs", nil, wire.LogSearchPage{}),
		"GET /api/v1/approvals":                                operation("List pending approvals", nil, wire.Approvals{}, query("pending", "boolean")),
		"GET /api/v1/runs/{run}/approvals":                     operation("Read run approvals", nil, wire.Approvals{}),
		"POST /api/v1/runs/{run}/approvals/{job}":              operation("Resolve an approval", wire.ApprovalDecision{}, wire.ApprovalResult{}),
		"POST /api/v1/runs/{run}/jobs/{job}/annotations":       operation("Add a job annotation", wire.AnnotationWrite{}, nil),
		"POST /api/v1/runs/{run}/jobs/{job}/steps/annotations": operation("Add a step annotation", wire.AnnotationWrite{}, nil),
		"GET /api/v1/sizing":                                   operation("List pipeline sizing", nil, wire.SizingPage{}, query("pipeline", "string"), query("limit", "integer"), query("cursor", "string")),
		"GET /api/v1/pools":                                    operation("List reserved pools and rules", nil, wire.Pools{}),
		"POST /api/v1/pools/{pool}/rules":                      operation("Add a pool rule", wire.PoolRuleWrite{}, wire.PoolRuleResult{}),
		"DELETE /api/v1/pools/{pool}/rules/{rule}":             operation("Remove a pool rule", nil, wire.PoolRuleDeletion{}),
		"GET /api/v1/secrets":                                  operation("List secret metadata", nil, wire.Secrets{}),
		"POST /api/v1/secrets":                                 operation("Write a secret or variable", wire.SecretWrite{}, wire.SecretWritten{}),
		"GET /api/v1/secrets/{name}":                           operation("Read an authorized secret value", nil, wire.SecretValue{}, query("pipeline", "string")),
		"DELETE /api/v1/secrets/{name}":                        operation("Delete an exact secret row", nil, wire.SecretDeletion{}, query("pipeline", "string")),
		"POST /api/v1/secrets/rotate":                          operation("Rotate encrypted secrets", wire.SecretRotationRequest{}, wire.SecretRotation{}),
		"GET /api/v1/runners":                                  operation("List granted runner observations", nil, wire.Runners{}),
		"GET /api/v1/schedules":                                operation("List schedules and health", nil, wire.ScheduleOverview{}, query("all", "boolean"), query("next", "integer")),
		"GET /api/v1/schedules/{schedule}":                     operation("Read a schedule and fire history", nil, wire.DetailView{}, query("fires", "integer"), query("next", "integer")),
		"DELETE /api/v1/schedules/{schedule}":                  operation("Delete a schedule", nil, wire.ScheduleDeletion{}),
		"POST /api/v1/schedules/{schedule}/pause":              operation("Pause a schedule", nil, wire.ScheduleEnvelope{}),
		"POST /api/v1/schedules/{schedule}/resume":             operation("Resume a schedule", nil, wire.ScheduleEnvelope{}),
		"PUT /api/v1/schedules/{schedule}/override":            operation("Replace a schedule override", wire.ScheduleOverride{}, wire.ScheduleEnvelope{}),
		"DELETE /api/v1/schedules/{schedule}/override":         operation("Reset a schedule override", nil, wire.ScheduleEnvelope{}),
		"POST /api/v1/schedules/{schedule}/run":                operation("Trigger a scheduled run", nil, wire.ScheduleRun{}),
		"PUT /api/v1/schedules/repos":                          operation("Register repository schedules", wire.ScheduleRepoRequest{}, wire.ScheduleRepoWritten{}),
		"DELETE /api/v1/schedules/repos":                       operation("Remove repository schedules", nil, wire.ScheduleRepoDeletion{}, query("repo_url", "string")),
		"GET /api/v1/queue":                                    operation("Read admission state", nil, wingwire.QueueState{}),
		"GET /api/v1/queue/groups/{group}":                     operation("Read a concurrency group", nil, wire.QueueGroup{}),
		"PUT /api/v1/queue/groups/{group}/priority":            operation("Change queued priority", wire.QueuePriorityWrite{}, wire.QueuePriorityResult{}),
		"POST /api/v1/data/source":                             operation("Reserve a source snapshot upload", wire.SourceUploadRequest{}, wire.DataUploadTicket{}),
		"POST /api/v1/data/source/{upload}/commit":             operation("Commit a verified source snapshot", struct{}{}, wire.DataReceipt{}),
		"POST /api/v1/data/upload":                             operation("Reserve an object upload", wire.DataUploadRequest{}, wire.DataUploadTicket{}),
		"POST /api/v1/data/commit":                             operation("Commit verified uploaded bytes", wire.DataCommitRequest{}, wire.DataReceipt{}),
		"POST /api/v1/data/download":                           operation("Sign an authorized object download", wire.DataDownloadRequest{}, wire.DataSignedTransfer{}),
		"POST /api/v1/data/has-many":                           operation("Read authorized object presence", wire.DataHasManyRequest{}, wire.DataPresence{}),
		"POST /node/v1/masks":                                  operation("Register a mask across host sinks", nodeproto.MaskRequest{}, nodeproto.ServiceAcknowledged{}),
		"POST /node/v1/annotations":                            operation("Write a masked annotation", nodeproto.AnnotationRequest{}, nodeproto.ServiceAcknowledged{}),
		"POST /node/v1/summary":                                operation("Write a masked summary", nodeproto.SummaryRequest{}, nodeproto.ServiceAcknowledged{}),
	}
	result["POST /api/v1/operator/teams"] = operation("Create a team and its first administrator", wire.OperatorTeamCreate{}, wire.OperatorTeamCreated{})
	result["POST /api/v1/operator/teams/{team}/refunds"] = operation("Record a completed provider refund", wire.OperatorRefund{}, wire.OperatorRefundRecorded{})
	result["GET /api/v1/runs/{run}/jobs/{job}/output"] = operation("Read an authorized committed job output", nil, wire.JobOutput{})
	result["GET /api/v1/runs/{run}/children/{child}/jobs/{job}/output"] = operation("Read an authorized child output", nil, wire.JobOutput{})

	for _, path := range []string{"/api/v1/runners/claim", "/api/v1/launcher/claim", "/api/v1/runs/{run}/claim", "/api/v1/runs/{run}/jobs/{job}/claim"} {
		result["POST "+path] = operation("Claim work without automatic replay", wire.ClaimRequest{}, wire.ClaimResponse{})
	}
	for _, action := range []string{"start", "finish", "skip"} {
		result["POST /api/v1/runs/{run}/jobs/{job}/steps/"+action] = operation("Persist an attempt-fenced step report", wire.StepReportRequest{}, wire.StepRecorded{})
	}
	for _, path := range []string{"/api/v1/tokens", "/api/v1/operator/tokens"} {
		result["GET "+path] = operation("List credential metadata", nil, struct {
			Tokens []capability.Metadata `json:"tokens"`
		}{}, query("kind", "string"), query("include_revoked", "string"))
		var create any = client.CreateTokenRequest{}
		if path == "/api/v1/operator/tokens" {
			create = client.CreateServiceTokenRequest{}
		}
		result["POST "+path] = operation("Issue a credential once", create, client.CreatedToken{})
		result["GET "+path+"/{token}"] = operation("Read credential metadata", nil, capability.Metadata{})
		result["DELETE "+path+"/{token}"] = operation("Revoke a credential", nil, nil)
		result["POST "+path+"/{token}/rotate"] = operation("Rotate a credential once", struct {
			GraceSecs int64      `json:"grace_secs"`
			ExpiresAt *time.Time `json:"expires_at,omitempty"`
		}{}, client.RotatedToken{})
	}
	result["POST /api/v1/runners"] = operation("Enroll a runner once", client.EnrollRunnerRequest{}, client.CreatedToken{})
	result["GET /api/v1/data/records"] = operation("Look up an authorized record", nil, wire.DataRecordLookupResult{}, query("kind", "string"), query("name", "string"), query("ref", "string"), query("pipeline", "string"), query("job", "string"), query("key", "string"), query("deadline", "string"), query("verified_default_only", "boolean"))
	result["POST /api/v1/data/records"] = operation("Commit a record", wire.DataRecordWriteRequest{}, wire.DataRecord{})
	addReadParameters(result)
	return result
}

func addReadParameters(operations map[string]wireOperation) {
	for _, key := range []string{"GET /api/v1/runs", "GET /api/v1/runs/grep"} {
		op := operations[key]
		for _, name := range []string{"parent_run_id", "cursor", "pipeline", "status", "sha", "error", "search"} {
			op.parameters = append(op.parameters, query(name, "string"))
		}
		for _, name := range []string{"started_after", "started_before", "finished_after", "finished_before"} {
			op.parameters = append(op.parameters, schema{"in": "query", "name": name, "schema": schema{"type": "string", "format": "date-time"}})
		}
		for _, name := range []string{"branch", "repo"} {
			op.parameters = append(op.parameters, schema{"in": "query", "name": name, "style": "form", "explode": true, "schema": schema{"type": "array", "items": schema{"type": "string"}}})
		}
		op.parameters = append(op.parameters, schema{"in": "query", "name": "limit", "required": true, "schema": schema{"type": "integer", "minimum": 1}}, query("after_number", "integer"), query("root_only", "boolean"))
		if key == "GET /api/v1/runs/grep" {
			op.parameters = append(op.parameters, query("grep", "string"), query("job", "string"), query("max_matches", "integer"))
		}
		operations[key] = op
	}
	for _, key := range []string{"GET /api/v1/runs/{run}/logs", "GET /api/v1/runs/{run}/jobs/{job}/logs", "GET /api/v1/runs/{run}/logs/search"} {
		op := operations[key]
		for _, name := range []string{"job", "cursor", "lines", "grep", "since"} {
			op.parameters = append(op.parameters, query(name, "string"))
		}
		for _, name := range []string{"tail", "head"} {
			op.parameters = append(op.parameters, query(name, "integer"))
		}
		for _, name := range []string{"tree", "events_only", "no_events"} {
			op.parameters = append(op.parameters, query(name, "boolean"))
		}
		operations[key] = op
	}
	key := "PUT /api/v1/queue/groups/{group}/priority"
	op := operations[key]
	op.parameters = append(op.parameters, schema{"in": "header", "name": "Idempotency-Key", "required": true, "schema": schema{"type": "string"}})
	operations[key] = op
}
