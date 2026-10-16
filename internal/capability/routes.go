package capability

// Routes is the authorization table for controller and node-host operations, including external proofs.
var Routes = []RoutePolicy{
	route("GET", "/api/v1/runs/{run}/local-host/data-authority", OwnerRun, RetrySafe, claim(DataRead, BindRunOfTrigger, LiveSensitive), claim(DataRead, BindPlanOwner, LiveSensitive), claim(DataRead, BindSourceReport, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/local-host/nodes/{job}/data-authority", OwnerRun, RetrySafe, claim(DataRead, BindOwnNode, LiveSensitive)),
	route("POST", "/api/v1/local/doctor", OwnerTeam, RetryNever, member(RunsControl)),
	route("GET", "/api/v1/runs/{run}/local-host/nodes/{job}/snapshot", OwnerRun, RetrySafe, claim(DataRead, BindOwnNode, LiveReporting), claim(DataRead, BindPlanOwner, LiveReporting), AccessRule{CredentialEval, []Grant{ExpansionSubmit}, BindRun, LiveExpansion}),
	route("POST", "/api/v1/runs/{run}/local-host/nodes/{job}/authentication", OwnerRun, RetrySafe, claim(DataRead, BindOwnNode, LiveReporting), claim(DataRead, BindPlanOwner, LiveReporting), AccessRule{CredentialEval, []Grant{ExpansionSubmit}, BindRun, LiveExpansion}),
	route("POST", "/api/v1/runs/{run}/local-host/nodes/{job}/binding", OwnerRun, RetrySafe, claim(DataRead, BindOwnNode, LiveReporting), claim(DataRead, BindPlanOwner, LiveReporting), AccessRule{CredentialEval, []Grant{ExpansionSubmit}, BindRun, LiveExpansion}),
	route("GET", "/api/v1/runs/{run}/local-host/snapshot", OwnerRun, RetrySafe, claim(TriggerHost, BindRunOfTrigger, LiveReporting), claim(DataRead, BindPlanOwner, LiveReporting), AccessRule{CredentialEval, []Grant{ExpansionSubmit}, BindRun, LiveExpansion}),
	route("GET", "/api/v1/runs/{run}/local-host/nodes/{job}", OwnerRun, RetrySafe, claim(TriggerHost, BindRunOfTrigger, LiveReporting), claim(DataRead, BindOwnNode, LiveReporting), claim(DataRead, BindPlanOwner, LiveReporting), AccessRule{CredentialEval, []Grant{ExpansionSubmit}, BindRun, LiveExpansion}),
	route("POST", "/api/v1/runs/{run}/local-host/transition", OwnerRun, RetryExactBody, claim(RunsState, BindRunOfTrigger, LiveResult)),
	route("POST", "/api/v1/runs/{run}/local-host/finish", OwnerRun, RetryExactBody, claim(RunsState, BindRunOfTrigger, LiveResult), claim(NodeResult, BindPlanOwner, LiveResult)),
	route("POST", "/api/v1/runs/{run}/local-host/nodes/{job}/transition", OwnerRun, RetryExactBody, claim(NodeResult, BindOwnNode, LiveResult), claim(NodeResult, BindRunOfTrigger, LiveResult)),
	route("POST", "/api/v1/runs/{run}/local-host/authentication", OwnerRun, RetrySafe, claim(TriggerHost, BindRunOfTrigger, LiveReporting), claim(DataRead, BindPlanOwner, LiveReporting), AccessRule{CredentialEval, []Grant{ExpansionSubmit}, BindRun, LiveExpansion}),
	route("POST", "/api/v1/runs/{run}/local-host/binding", OwnerRun, RetrySafe, claim(TriggerHost, BindRunOfTrigger, LiveReporting), claim(DataRead, BindPlanOwner, LiveReporting), AccessRule{CredentialEval, []Grant{ExpansionSubmit}, BindRun, LiveExpansion}),
	route("GET", "/api/v1/runs/{run}/snapshot", OwnerRun, RetrySafe, claim(TriggerHost, BindRunOfTrigger, LiveSensitive)),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/reconcile", OwnerRun, RetryNever, claim(NodesClaim, BindRunOfTrigger, LiveSensitive)),
	route("POST", "/api/v1/runs", OwnerTeam, RetryIdempotencyKey, intake(), claim(ChildrenStart, BindBodyNamesRun, LiveSensitive)),
	route("GET", "/api/v1/runs", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/runs/grep", OwnerTeam, RetrySafe, member(LogsRead)),
	route("GET", "/api/v1/runs/{run}", OwnerRun, RetrySafe, member(RunsRead), claim(SourceRead, BindRun, LiveSensitive), claim(TriggerHost, BindRun, LiveSensitive)),
	route("DELETE", "/api/v1/runs/{run}", OwnerRun, RetrySafe, member(RunsControl)),
	route("POST", "/api/v1/runs/{run}/cancel", OwnerRun, RetrySafe, member(RunsControl)),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/cancel", OwnerRun, RetrySafe, member(RunsControl), claim(TriggerHost, BindRunOfTrigger, LiveReporting)),
	route("GET", "/api/v1/runs/{run}/jobs/{job}/failure", OwnerRun, RetrySafe, claim(DataRead, BindReadSet, LiveSensitive)),
	route("POST", "/api/v1/runs/{run}/retry", OwnerRun, RetryIdempotencyKey, member(RunsControl)),
	route("GET", "/api/v1/runs/{run}/receipt", OwnerRun, RetrySafe, member(RunsRead), claim(SourceRead, BindRun, LiveSensitive), claim(TriggerHost, BindRun, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/attempts", OwnerRun, RetrySafe, member(RunsRead), claim(SourceRead, BindRun, LiveSensitive), claim(TriggerHost, BindRun, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/steps", OwnerRun, RetrySafe, member(RunsRead), claim(SourceRead, BindRun, LiveSensitive), claim(TriggerHost, BindRun, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/events", OwnerRun, RetrySafe, member(RunsRead), claim(SourceRead, BindRun, LiveSensitive), claim(TriggerHost, BindRun, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/events/stream", OwnerRun, RetrySafe, member(RunsRead), claim(SourceRead, BindRun, LiveSensitive), claim(TriggerHost, BindRun, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/approvals", OwnerRun, RetrySafe, member(RunsRead), claim(SourceRead, BindRun, LiveSensitive), claim(TriggerHost, BindRun, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/debug-pauses", OwnerRun, RetrySafe, member(RunsRead), claim(SourceRead, BindRun, LiveSensitive), claim(TriggerHost, BindRun, LiveSensitive)),
	route("POST", "/api/v1/runs/{run}/debug-pauses", OwnerRun, RetryIdempotencyKey, member(RunsControl)),
	route("GET", "/api/v1/runs/{run}/jobs", OwnerRun, RetrySafe, member(RunsRead), claim(TriggerHost, BindRun, LiveSensitive)),
	route("POST", "/api/v1/runs/{run}/plan", OwnerRun, RetryExactBody, claim(NodeResult, BindPlanOwner, LiveResult), publisher()),
	route("POST", "/api/v1/runs/{run}/finish", OwnerRun, RetryExactBody, claim(RunsState, BindRunOfTrigger, LiveResult), publisher()),
	route("POST", "/api/v1/runs/{run}/heartbeat", OwnerRun, RetrySafe, claim(TriggerHost, BindRunOfTrigger, LiveReporting)),
	route("POST", "/api/v1/runs/{run}/claim", OwnerRun, RetryNever, hereClaim(), claimant(TriggersClaim)),
	route("POST", "/api/v1/runs/{run}/source-credential", OwnerRun, RetryNever, claim(SourceRead, BindSource, LiveSensitive), AccessRule{Credential: CredentialBootstrap, Grants: []Grant{SourceRead}, Binding: BindSource, Liveness: LiveSensitive}),
	route("POST", "/api/v1/runs/{run}/oidc-token", OwnerRun, RetryNever, claim(OIDCMint, BindRun, LiveSensitive)),
	route("POST", "/api/v1/runs/{run}/children", OwnerRun, RetryIdempotencyKey, claim(ChildrenStart, BindBodyNamesRun, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/children/{child}", OwnerRun, RetrySafe, claim(ChildrenStart, BindChildOfBoundNode, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/children/{child}/jobs/{job}/output", OwnerRun, RetrySafe, claim(RefsRead, BindChildOfBoundNode, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/logs", OwnerRun, RetrySafe, member(LogsRead), claim(TriggerHost, BindRun, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/logs/search", OwnerRun, RetrySafe, member(LogsRead), claim(TriggerHost, BindRun, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/jobs/{job}", OwnerRun, RetrySafe, member(RunsRead), claim(DataRead, BindOwnNode, LiveSensitive), claim(TriggerHost, BindRunOfTrigger, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/jobs/{job}/attempts", OwnerRun, RetrySafe, member(RunsRead), claim(DataRead, BindOwnNode, LiveSensitive), claim(TriggerHost, BindRunOfTrigger, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/jobs/{job}/steps", OwnerRun, RetrySafe, member(RunsRead), claim(DataRead, BindOwnNode, LiveSensitive), claim(TriggerHost, BindRunOfTrigger, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/jobs/{job}/metrics", OwnerRun, RetrySafe, member(RunsRead), claim(DataRead, BindOwnNode, LiveSensitive), claim(TriggerHost, BindRunOfTrigger, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/jobs/{job}/debug-pause", OwnerRun, RetrySafe, member(RunsRead), claim(DataRead, BindOwnNode, LiveSensitive), claim(TriggerHost, BindRunOfTrigger, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/jobs/{job}/logs", OwnerRun, RetrySafe, member(LogsRead), claim(DataRead, BindOwnNode, LiveSensitive), claim(TriggerHost, BindRunOfTrigger, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/jobs/{job}/logs/stream", OwnerRun, RetrySafe, member(LogsRead), claim(DataRead, BindOwnNode, LiveSensitive), claim(TriggerHost, BindRunOfTrigger, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/jobs/{job}/logs/completeness", OwnerRun, RetrySafe, member(LogsRead), claim(DataRead, BindOwnNode, LiveSensitive), claim(TriggerHost, BindRunOfTrigger, LiveSensitive)),
	route("GET", "/api/v1/runs/{run}/jobs/{job}/output", OwnerRun, RetrySafe, claim(DataRead, BindReadSet, LiveSensitive), claim(DataRead, BindReadSet, LiveExpansion), claim(RefsRead, BindReadSet, LiveSensitive), member(RunsRead)),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/transition", OwnerRun, RetryExactBody, claim(NodeResult, BindOwnNode, LiveResult), claim(NodeResult, BindRunOfTrigger, LiveResult), publisher()),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/claim", OwnerRun, RetryNever, claim(NodesClaim, BindRunOfTrigger, LiveSensitive)),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/claim/input", OwnerRun, RetrySafe, claim(DataRead, BindOwnNode, LiveSensitive)),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/heartbeat", OwnerRun, RetrySafe, claim(NodeReport, BindOwnNode, LiveReporting), claim(NodeReport, BindRunOfTrigger, LiveReporting)),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/expansion", OwnerRun, RetryExactBody, claim(ExpansionSubmit, BindExpansion, LiveExpansion), claim(ExpansionSubmit, BindRunOfTrigger, LiveSensitive)),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/logs", OwnerRun, RetryExactBody, claim(NodeReport, BindOwnNode, LiveReporting), claim(NodeReport, BindRunOfTrigger, LiveReporting), publisher()),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/metrics", OwnerRun, RetryExactBody, claim(NodeReport, BindOwnNode, LiveReporting), claim(NodeReport, BindRunOfTrigger, LiveReporting), publisher()),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/usage", OwnerRun, RetryExactBody, claim(NodeReport, BindOwnNode, LiveReporting), claim(NodeReport, BindRunOfTrigger, LiveReporting), publisher()),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/activity", OwnerRun, RetryExactBody, claim(NodeReport, BindOwnNode, LiveReporting), claim(NodeReport, BindRunOfTrigger, LiveReporting), publisher()),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/annotations", OwnerRun, RetryExactBody, claim(NodeReport, BindOwnNode, LiveReporting), claim(NodeReport, BindRunOfTrigger, LiveReporting), claim(NodeReport, BindPlanOwner, LiveReporting), claim(ExpansionSubmit, BindSourceReport, LiveExpansion), publisher()),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/summary", OwnerRun, RetryExactBody, claim(NodeReport, BindOwnNode, LiveReporting), claim(NodeReport, BindRunOfTrigger, LiveReporting), claim(NodeReport, BindPlanOwner, LiveReporting), claim(ExpansionSubmit, BindSourceReport, LiveExpansion), publisher()),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/steps/start", OwnerRun, RetryExactBody, claim(NodeReport, BindOwnNode, LiveReporting), claim(NodeReport, BindRunOfTrigger, LiveReporting), publisher()),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/steps/finish", OwnerRun, RetryExactBody, claim(NodeReport, BindOwnNode, LiveReporting), claim(NodeReport, BindRunOfTrigger, LiveReporting), publisher()),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/steps/skip", OwnerRun, RetryExactBody, claim(NodeReport, BindOwnNode, LiveReporting), claim(NodeReport, BindRunOfTrigger, LiveReporting), publisher()),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/steps/annotations", OwnerRun, RetryExactBody, claim(NodeReport, BindOwnNode, LiveReporting), claim(NodeReport, BindRunOfTrigger, LiveReporting), publisher()),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/steps/summary", OwnerRun, RetryExactBody, claim(NodeReport, BindOwnNode, LiveReporting), claim(NodeReport, BindRunOfTrigger, LiveReporting), publisher()),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/log-segments", OwnerRun, RetryExactBody, claim(LogTail, BindOwnNode, LiveLogTail), claim(LogTail, BindRunOfTrigger, LiveLogTail), publisher()),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/log-manifest", OwnerRun, RetryExactBody, claim(LogTail, BindOwnNode, LiveLogTail), claim(LogTail, BindRunOfTrigger, LiveLogTail), publisher()),
	route("POST", "/api/v1/runs/{run}/jobs/{job}/resume", OwnerRun, RetrySafe, member(RunsControl)),
	route("GET", "/api/v1/runs/{run}/approvals/{job}", OwnerRun, RetrySafe, member(RunsRead), claim(DataRead, BindOwnNode, LiveSensitive), claim(TriggerHost, BindRunOfTrigger, LiveSensitive)),
	route("POST", "/api/v1/runs/{run}/approvals/{job}", OwnerRun, RetryIdempotencyKey, member(ApprovalsWrite)),
	route("GET", "/api/v1/approvals", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/pipelines", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/pipelines/{name}/latest", OwnerTeam, RetrySafe, member(RunsRead), claim(RefsRead, BindReadSet, LiveSensitive)),
	route("GET", "/api/v1/pipelines/{name}/sizing", OwnerTeam, RetrySafe, member(RunsRead), claim(SourceRead, BindPipelineOfBoundRun, LiveSensitive)),
	route("PUT", "/api/v1/pipelines/{name}/sizing/pin", OwnerTeam, RetryExactBody, claim(NodeReport, BindPipelineOfBoundRun, LiveReporting)),
	route("POST", "/api/v1/pipelines/{name}/sizing/observations", OwnerTeam, RetryExactBody, claim(NodeReport, BindPipelineOfBoundRun, LiveReporting)),
	route("POST", "/api/v1/pipelines/{name}/sizing/contention", OwnerTeam, RetryExactBody, claim(NodeReport, BindPipelineOfBoundRun, LiveReporting)),
	route("POST", "/api/v1/pipelines/{name}/sizing/waits", OwnerTeam, RetryExactBody, claim(NodeReport, BindPipelineOfBoundRun, LiveReporting)),
	route("GET", "/api/v1/sizing", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/sizing/explain", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/runners", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/runners/github", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/pools", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/schedules", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/schedules/{schedule}", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/queue", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/queue/groups/{group}", OwnerTeam, RetrySafe, member(RunsRead), claim(NodeReport, BindSlotOfBoundNode, LiveReporting), claim(RunsState, BindSlotOfBoundNode, LiveReporting)),
	route("GET", "/api/v1/trends", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/storage", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/credits", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/credits/history", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/credits/settings", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/team/billing", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/team/build-trust", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/team/git-credentials", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/team/github-app", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/team/github-app/installations/{installation}/repositories", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/team/github-app/triggers", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/team/github-app/automation", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/team/github-app/extra-repos", OwnerTeam, RetrySafe, member(RunsRead)),
	route("GET", "/api/v1/team/members", OwnerTeam, RetrySafe, member(RunsRead)),
	route("POST", "/api/v1/runners", OwnerTeam, RetryNever, member(TeamAdmin)),
	route("DELETE", "/api/v1/runners/{runner}", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("POST", "/api/v1/runners/github", OwnerTeam, RetryIdempotencyKey, member(TeamAdmin)),
	route("DELETE", "/api/v1/runners/github/{repository}", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("POST", "/api/v1/pools/{pool}/rules", OwnerTeam, RetryIdempotencyKey, member(TeamAdmin)),
	route("DELETE", "/api/v1/pools/{pool}/rules/{rule}", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("PUT", "/api/v1/schedules/local/repos", OwnerTeam, RetryNever, member(RunsControl)),
	route("POST", "/api/v1/schedules/local/tick", OwnerTeam, RetryNever, member(RunsControl)),
	route("POST", "/api/v1/schedules/{schedule}/pin", OwnerTeam, RetryNever, member(RunsControl)),
	route("POST", "/api/v1/schedules/{schedule}/unpin", OwnerTeam, RetrySafe, member(RunsControl)),
	route("PUT", "/api/v1/schedules/repos", OwnerTeam, RetrySafe, member(RunsControl)),
	route("DELETE", "/api/v1/schedules/repos", OwnerTeam, RetrySafe, member(RunsControl)),
	route("PUT", "/api/v1/schedules/{schedule}/override", OwnerTeam, RetrySafe, member(RunsControl)),
	route("DELETE", "/api/v1/schedules/{schedule}/override", OwnerTeam, RetrySafe, member(RunsControl)),
	route("DELETE", "/api/v1/schedules/{schedule}", OwnerTeam, RetrySafe, member(RunsControl)),
	route("POST", "/api/v1/schedules/{schedule}/pause", OwnerTeam, RetrySafe, member(RunsControl)),
	route("POST", "/api/v1/schedules/{schedule}/resume", OwnerTeam, RetrySafe, member(RunsControl)),
	route("POST", "/api/v1/schedules/{schedule}/run", OwnerTeam, RetryIdempotencyKey, member(RunsControl)),
	route("PUT", "/api/v1/queue/groups/{group}/priority", OwnerTeam, RetrySafe, member(RunsControl)),
	route("POST", "/api/v1/queue/groups/{group}/force-release", OwnerTeam, RetrySafe, member(RunsControl)),
	route("POST", "/api/v1/queue/groups/{group}/acquire", OwnerTeam, RetryIdempotencyKey, claim(NodeReport, BindSlotOfBoundNode, LiveSensitive), claim(RunsState, BindSlotOfBoundNode, LiveSensitive)),
	route("POST", "/api/v1/queue/groups/{group}/heartbeat", OwnerTeam, RetrySafe, claim(NodeReport, BindSlotOfBoundNode, LiveReporting), claim(RunsState, BindSlotOfBoundNode, LiveReporting)),
	route("POST", "/api/v1/queue/groups/{group}/release", OwnerTeam, RetrySafe, claim(NodeReport, BindSlotOfBoundNode, LiveReporting), claim(RunsState, BindSlotOfBoundNode, LiveReporting)),
	route("POST", "/api/v1/queue/groups/{group}/cancel-waiter", OwnerTeam, RetrySafe, claim(NodeReport, BindSlotOfBoundNode, LiveReporting), claim(RunsState, BindSlotOfBoundNode, LiveReporting)),
	route("GET", "/api/v1/queue/groups/{group}/holder", OwnerTeam, RetrySafe, claim(NodeReport, BindSlotOfBoundNode, LiveReporting), claim(RunsState, BindSlotOfBoundNode, LiveReporting)),
	route("GET", "/api/v1/queue/groups/{group}/resolve", OwnerTeam, RetrySafe, claim(NodeReport, BindSlotOfBoundNode, LiveSensitive), claim(RunsState, BindSlotOfBoundNode, LiveSensitive)),
	route("GET", "/api/v1/tokens", OwnerTeam, RetrySafe, member(RunsRead)),
	route("POST", "/api/v1/tokens", OwnerTeam, RetryNever, member(RunsRead)),
	route("GET", "/api/v1/tokens/{token}", OwnerTeam, RetrySafe, tokenOwner()),
	route("DELETE", "/api/v1/tokens/{token}", OwnerTeam, RetrySafe, tokenOwner()),
	route("POST", "/api/v1/tokens/{token}/rotate", OwnerTeam, RetryNever, tokenOwner()),
	route("PUT", "/api/v1/tokens/{token}/git-credentials", OwnerTeam, RetrySafe, machineOwner()),
	route("GET", "/api/v1/secrets", OwnerTeam, RetrySafe, member(RunsRead), member(TeamAdmin)),
	route("POST", "/api/v1/secrets", OwnerTeam, RetryIdempotencyKey, member(TeamAdmin)),
	route("GET", "/api/v1/secrets/{name}", OwnerTeam, RetrySafe, member(SecretsRead), member(TeamAdmin), claim(SecretsRelease, BindDeclaredSecret, LiveSensitive)),
	route("DELETE", "/api/v1/secrets/{name}", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("POST", "/api/v1/secrets/rotate", OwnerTeam, RetryIdempotencyKey, member(TeamAdmin)),
	route("PATCH", "/api/v1/team", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("DELETE", "/api/v1/team", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("PATCH", "/api/v1/team/members/{account}", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("GET", "/api/v1/team/invitations", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("POST", "/api/v1/team/invitations", OwnerTeam, RetryIdempotencyKey, member(TeamAdmin)),
	route("DELETE", "/api/v1/team/invitations/{invitation}", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("PUT", "/api/v1/team/build-trust", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("POST", "/api/v1/team/git-credentials", OwnerTeam, RetryIdempotencyKey, member(TeamAdmin)),
	route("POST", "/api/v1/team/git-credentials/{host}/confirm", OwnerTeam, RetryIdempotencyKey, member(TeamAdmin)),
	route("DELETE", "/api/v1/team/git-credentials/{host}", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("GET", "/api/v1/team/git-credentials/releases", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("DELETE", "/api/v1/team/github-app/installations/{installation}", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("PUT", "/api/v1/team/github-app/triggers", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("DELETE", "/api/v1/team/github-app/triggers", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("PUT", "/api/v1/team/github-app/automation", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("DELETE", "/api/v1/team/github-app/automation", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("PUT", "/api/v1/team/github-app/extra-repos", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("POST", "/api/v1/team/billing/checkout", OwnerTeam, RetryIdempotencyKey, member(TeamAdmin)),
	route("POST", "/api/v1/team/billing/card", OwnerTeam, RetryIdempotencyKey, member(TeamAdmin)),
	route("POST", "/api/v1/team/billing/pay", OwnerTeam, RetryIdempotencyKey, member(TeamAdmin)),
	route("PUT", "/api/v1/team/billing/budget", OwnerTeam, RetrySafe, member(TeamAdmin)),
	route("DELETE", "/api/v1/team/members/{account}", OwnerTeam, RetrySafe, selfOrTeamAdmin()),
	route("GET", "/api/v1/me", OwnerAccount, RetrySafe, account()),
	route("GET", "/api/v1/me/team-deletions", OwnerAccount, RetrySafe, account()),
	route("POST", "/api/v1/me/active-team", OwnerAccount, RetryIdempotencyKey, account()),
	route("GET", "/api/v1/me/identities", OwnerAccount, RetrySafe, account()),
	route("DELETE", "/api/v1/me/identities/{provider}", OwnerAccount, RetrySafe, account()),
	route("POST", "/api/v1/teams", OwnerAccount, RetryIdempotencyKey, account()),
	route("POST", "/api/v1/invitations/{invitation}/accept", OwnerAccount, RetryIdempotencyKey, account()),
	route("GET", "/api/v1/auth/whoami", OwnerNone, RetrySafe, authenticated()),
	route("GET", "/api/v1/services", OwnerTeam, RetrySafe, authenticated()),
	route("POST", "/api/v1/runners/claim", OwnerTeam, RetryNever, claimant(NodesClaim), claimant(TriggersClaim)),
	route("POST", "/api/v1/launcher/claim", OwnerDeployment, RetryNever, launcher()),
	route("POST", "/api/v1/launcher/sync", OwnerDeployment, RetrySafe, launcher()),
	route("POST", "/api/v1/claims/exchange", OwnerRun, RetryNever, AccessRule{Credential: CredentialBootstrap, Grants: []Grant{ClaimExchange}, Binding: BindRun, Liveness: LiveSensitive}),
	route("POST", "/api/v1/claims/{claim}/heartbeat", OwnerRun, RetrySafe, claim(NodeReport, BindOwnClaim, LiveReporting), evalUnit()),
	route("POST", "/api/v1/claims/{claim}/logs", OwnerRun, RetryExactBody, claim(LogTail, BindOwnClaim, LiveLogTail), evalUnit()),
	route("POST", "/api/v1/claims/{claim}/log-segments", OwnerRun, RetryExactBody, claim(LogTail, BindOwnClaim, LiveLogTail), evalUnit()),
	route("POST", "/api/v1/claims/{claim}/log-manifest", OwnerRun, RetryExactBody, claim(LogTail, BindOwnClaim, LiveLogTail), evalUnit()),
	route("POST", "/api/v1/data/download", OwnerTeam, RetrySafe, claim(DataRead, BindData, LiveSensitive), claim(DataRead, BindData, LiveExpansion), repoReader()),
	route("POST", "/api/v1/data/upload", OwnerTeam, RetryIdempotencyKey, claim(DataWrite, BindData, LiveSensitive), publisher()),
	route("POST", "/api/v1/data/commit", OwnerTeam, RetryExactBody, claim(DataWrite, BindData, LiveSensitive), publisher()),
	route("POST", "/api/v1/data/records", OwnerTeam, RetryExactBody, claim(DataWrite, BindData, LiveSensitive), publisher()),
	route("POST", "/api/v1/data/has-many", OwnerTeam, RetrySafe, claim(DataRead, BindData, LiveSensitive), claim(DataRead, BindData, LiveExpansion), repoReader()),
	route("GET", "/api/v1/data/records", OwnerTeam, RetrySafe, claim(DataRead, BindData, LiveSensitive), claim(DataRead, BindData, LiveExpansion), repoReader()),
	route("POST", "/api/v1/data/memo/lookup", OwnerTeam, RetrySafe, claim(DataRead, BindData, LiveSensitive), claim(DataRead, BindData, LiveExpansion), repoReader()),
	route("POST", "/api/v1/data/depcache/lookup", OwnerTeam, RetrySafe, claim(DataRead, BindData, LiveSensitive), claim(DataRead, BindData, LiveExpansion), repoReader()),
	route("POST", "/api/v1/data/memo/copy", OwnerTeam, RetryExactBody, AccessRule{CredentialClaim, []Grant{DataRead, DataWrite}, BindData, LiveSensitive}),
	route("GET", "/api/v1/data/capabilities", OwnerTeam, RetrySafe, authenticated()),
	route("GET", "/api/v1/operator/limits", OwnerDeployment, RetrySafe, deployment(OperatorLimits), console()),
	route("PUT", "/api/v1/operator/limits", OwnerDeployment, RetrySafe, deployment(OperatorLimits), console()),
	route("GET", "/api/v1/operator/signups", OwnerDeployment, RetrySafe, deployment(OperatorLimits), console()),
	route("PUT", "/api/v1/operator/signups", OwnerDeployment, RetrySafe, deployment(OperatorLimits), console()),
	route("GET", "/api/v1/operator/object-store", OwnerDeployment, RetrySafe, deployment(OperatorObjectStore), console()),
	route("GET", "/api/v1/operator/object-store/breaker", OwnerDeployment, RetrySafe, deployment(OperatorObjectStore), console()),
	route("POST", "/api/v1/operator/object-store/reset-breaker", OwnerDeployment, RetryIdempotencyKey, deployment(OperatorObjectStore), console()),
	route("PUT", "/api/v1/operator/object-store/settings", OwnerDeployment, RetrySafe, deployment(OperatorObjectStore), console()),
	route("PUT", "/api/v1/operator/object-store/quotas/{team}", OwnerDeployment, RetrySafe, deployment(OperatorObjectStore), console()),
	route("PUT", "/api/v1/operator/object-store/quotas/{team}/allowance", OwnerDeployment, RetrySafe, deployment(OperatorObjectStore), console()),
	route("GET", "/api/v1/operator/tokens", OwnerDeployment, RetrySafe, deployment(OperatorTokens), console()),
	route("POST", "/api/v1/operator/tokens", OwnerDeployment, RetryNever, deployment(OperatorTokens), console()),
	route("GET", "/api/v1/operator/tokens/{token}", OwnerDeployment, RetrySafe, deployment(OperatorTokens), console()),
	route("DELETE", "/api/v1/operator/tokens/{token}", OwnerDeployment, RetrySafe, deployment(OperatorTokens), console()),
	route("POST", "/api/v1/operator/tokens/{token}/rotate", OwnerDeployment, RetryNever, deployment(OperatorTokens), console()),
	route("GET", "/api/v1/operator/teams", OwnerDeployment, RetrySafe, console()),
	route("GET", "/api/v1/operator/teams/{team}", OwnerDeployment, RetrySafe, console()),
	route("DELETE", "/api/v1/operator/teams/{team}", OwnerDeployment, RetrySafe, console()),
	route("POST", "/api/v1/operator/teams/{team}/trust", OwnerDeployment, RetryIdempotencyKey, console()),
	route("POST", "/api/v1/operator/teams/{team}/grants", OwnerDeployment, RetryIdempotencyKey, console()),
	route("POST", "/api/v1/operator/teams/{team}/freeze", OwnerDeployment, RetryIdempotencyKey, console()),
	route("POST", "/api/v1/operator/teams/{team}/unfreeze", OwnerDeployment, RetryIdempotencyKey, console()),
	route("POST", "/api/v1/operator/teams/{team}/refunds", OwnerDeployment, RetryIdempotencyKey, console()),
	route("DELETE", "/api/v1/operator/accounts/{account}", OwnerDeployment, RetrySafe, console()),
	route("GET", "/api/v1/operator/waitlist", OwnerDeployment, RetrySafe, console()),
	route("POST", "/api/v1/operator/waitlist/approve", OwnerDeployment, RetryIdempotencyKey, console()),
	route("DELETE", "/api/v1/operator/github-app/installations/{installation}", OwnerDeployment, RetrySafe, console()),
	route("PUT", "/api/v1/operator/credits/settings", OwnerDeployment, RetrySafe, console()),
	route("POST", "/api/v1/operator/runners/{runner}/metered", OwnerDeployment, RetryIdempotencyKey, console()),
	externalRoute("GET", "/api/v1/health", AuthPublic, OwnerNone, RetrySafe),
	externalRoute("GET", "/api/v1/capabilities", AuthPublic, OwnerNone, RetrySafe),
	externalRoute("POST", "/webhooks/stripe", AuthPaymentWebhook, OwnerTeam, RetryExactBody),
	externalRoute("POST", "/webhooks/github-app", AuthGitHubWebhook, OwnerTeam, RetryExactBody),
	externalRoute("POST", "/api/v1/runners/github/exchange", AuthGitHubActions, OwnerTeam, RetryNever),
	externalRoute("GET", "/.well-known/openid-configuration", AuthPublic, OwnerNone, RetrySafe),
	externalRoute("GET", "/.well-known/jwks.json", AuthPublic, OwnerNone, RetrySafe),
	externalRoute("PUT", "/api/v1/data/uploads/{upload}", AuthSignedObject, OwnerTeam, RetryExactBody),
	externalRoute("GET", "/api/v1/data/objects/{key...}", AuthSignedObject, OwnerTeam, RetrySafe),
	externalRoute("PUT", "/api/v1/log-segments/{key...}", AuthSignedObject, OwnerRun, RetryExactBody),
	externalRoute("GET", "/api/v1/auth/session", AuthBrowserFlow, OwnerAccount, RetryNever),
	externalRoute("POST", "/api/v1/auth/logout", AuthBrowserFlow, OwnerAccount, RetryNever),
	externalRoute("GET", "/login", AuthBrowserFlow, OwnerAccount, RetryNever),
	externalRoute("POST", "/login", AuthBrowserFlow, OwnerAccount, RetryNever),
	externalRoute("POST", "/logout", AuthBrowserFlow, OwnerAccount, RetryNever),
	externalRoute("GET", "/auth/{provider}/start", AuthBrowserFlow, OwnerAccount, RetryNever),
	externalRoute("GET", "/auth/{provider}/callback", AuthBrowserFlow, OwnerAccount, RetryNever),
	externalRoute("POST", "/auth/{provider}/link", AuthBrowserFlow, OwnerAccount, RetryNever),
	externalRoute("GET", "/auth/{provider}/link/complete", AuthBrowserFlow, OwnerAccount, RetryNever),
	externalRoute("POST", "/github/app/connect", AuthBrowserFlow, OwnerAccount, RetryNever),
	externalRoute("POST", "/github/app/connect/existing", AuthBrowserFlow, OwnerAccount, RetryNever),
	externalRoute("GET", "/github/app/setup", AuthBrowserFlow, OwnerAccount, RetryNever),
	externalRoute("GET", "/github/app/callback", AuthBrowserFlow, OwnerAccount, RetryNever),
	externalRoute("GET", "/github/app/complete", AuthBrowserFlow, OwnerAccount, RetryNever),
	externalRoute("GET", "/github/app/available", AuthBrowserFlow, OwnerAccount, RetryNever),
	externalRoute("POST", "/github/app/select", AuthBrowserFlow, OwnerAccount, RetryNever),
	route("GET", "/metrics", OwnerDeployment, RetrySafe, deployment(OperatorLimits), console()),
	nodeRoute("GET", "/node/v1/secrets/{name}", SecretsRelease, BindDeclaredSecret, RetrySafe),
	nodeRoute("GET", "/node/v1/jobs/{job}/output", DataRead, BindReadSet, RetrySafe),
	nodeRoute("GET", "/node/v1/pipelines/{name}/latest", RefsRead, BindReadSet, RetrySafe),
	nodeRoute("POST", "/node/v1/oidc-token", OIDCMint, BindOwnNode, RetryNever),
	nodeRoute("POST", "/node/v1/masks", NodeReport, BindOwnNode, RetryExactBody),
	nodeRoute("POST", "/node/v1/annotations", NodeReport, BindOwnNode, RetryNever),
	nodeRoute("POST", "/node/v1/summary", NodeReport, BindOwnNode, RetryExactBody),
	nodeRoute("POST", "/node/v1/children", ChildrenStart, BindBodyNamesRun, RetryNever),
	nodeRoute("GET", "/node/v1/children/{run}/await", ChildrenStart, BindChildOfBoundNode, RetrySafe),
	nodeRoute("POST", "/node/v1/cleanups", NodeReport, BindOwnCleanup, RetryNever),
	nodeRoute("DELETE", "/node/v1/cleanups/{cleanup}", NodeReport, BindOwnCleanup, RetryNever),
}

func route(method, path string, owner Owner, retry RetryPolicy, access ...AccessRule) RoutePolicy {
	return RoutePolicy{Method: method, Path: path, Audience: Controller, Owner: owner, Authentication: AuthCapability, Access: access, Retry: retry}
}

func externalRoute(method, path string, auth Authentication, owner Owner, retry RetryPolicy) RoutePolicy {
	return RoutePolicy{Method: method, Path: path, Audience: Controller, Owner: owner, Authentication: auth, Retry: retry}
}

func nodeRoute(method, path string, grant Grant, binding BindingRule, retry RetryPolicy) RoutePolicy {
	ownerBinding := binding
	if binding == BindOwnNode || binding == BindOwnCleanup {
		ownerBinding = BindRunOfTrigger
	}
	access := []AccessRule{{CredentialNodeProcess, []Grant{grant}, binding, LiveProcess}, {CredentialOwnerProcess, []Grant{grant}, ownerBinding, LiveProcess}}
	if grant == NodeReport {
		access = append(access, AccessRule{CredentialOwnerProcess, []Grant{ExpansionSubmit}, BindSourceReport, LiveProcess}, AccessRule{CredentialOwnerProcess, []Grant{NodeReport}, BindPlanOwner, LiveProcess})
	}
	return RoutePolicy{
		Method: method, Path: path, Audience: NodeHost, Owner: OwnerNodeHost, Authentication: AuthCapability,
		Access: access, Retry: retry,
	}
}

func member(grant Grant) AccessRule {
	return AccessRule{CredentialMember, []Grant{grant}, BindNone, LiveCredential}
}

func claim(grant Grant, binding BindingRule, live Liveness) AccessRule {
	return AccessRule{CredentialClaim, []Grant{grant}, binding, live}
}
func account() AccessRule { return AccessRule{CredentialAccount, nil, BindNone, LiveCredential} }
func console() AccessRule {
	return AccessRule{CredentialOperator, []Grant{OperatorConsole}, BindNone, LiveCredential}
}

func deployment(grant Grant) AccessRule {
	return AccessRule{CredentialDeployment, []Grant{grant}, BindNone, LiveCredential}
}

func authenticated() AccessRule {
	return AccessRule{CredentialDiscovery, nil, BindNone, LiveCredential}
}

func launcher() AccessRule {
	return AccessRule{CredentialLauncher, []Grant{ClaimsLaunch}, BindClaimSelection, LiveCredential}
}

func claimant(grant Grant) AccessRule {
	return AccessRule{CredentialAny, []Grant{grant}, BindClaimSelection, LiveCredential}
}

func hereClaim() AccessRule {
	return AccessRule{CredentialHere, []Grant{TriggerHost}, BindRunOfTrigger, LiveHereClaim}
}

func intake() AccessRule {
	return AccessRule{CredentialMember, []Grant{RunsWrite}, BindBodyNamesRun, LiveCredential}
}

func tokenOwner() AccessRule {
	return AccessRule{CredentialMember, []Grant{RunsRead}, BindOwnToken, LiveCredential}
}

func machineOwner() AccessRule {
	return AccessRule{CredentialAccount, nil, BindMachineOwner, LiveCredential}
}

func selfOrTeamAdmin() AccessRule {
	return AccessRule{CredentialMember, []Grant{RunsRead}, BindSelfOrTeamAdmin, LiveCredential}
}

func publisher() AccessRule {
	return AccessRule{CredentialPublisher, []Grant{DataWrite}, BindData, LiveCredential}
}

func repoReader() AccessRule {
	return AccessRule{CredentialRepoReader, []Grant{DataRead}, BindData, LiveCredential}
}

// safety: evalUnit confines expansion authority to the eval lease's own logs and heartbeat.
func evalUnit() AccessRule {
	return AccessRule{Credential: CredentialEval, Grants: []Grant{ExpansionSubmit}, Binding: BindOwnEval, Liveness: LiveExpansion}
}
