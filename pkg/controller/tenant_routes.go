package controller

type tenantOwner int

const (
	ownerNone tenantOwner = iota
	// safety: a run and its trigger share one id, so one ownership check covers both.
	ownerRun
	// safety: another team's row under the same name is a different row, so
	// resolving the caller's team is the whole check.
	ownerTeam
	// safety: accountPrincipal in each handler refuses every caller that is not the signed-in account.
	ownerAccount
	// safety: the path names a row of any team on purpose, so a test holds each
	// such route to deployment or service authority.
	ownerDeployment
)

type tenantResource struct {
	name  string
	owner tenantOwner
}

var (
	resNone               = tenantResource{"none", ownerNone}
	resRun                = tenantResource{"run", ownerRun}
	resTrigger            = tenantResource{"trigger", ownerRun}
	resSecret             = tenantResource{"secret", ownerTeam}
	resCron               = tenantResource{"cron", ownerTeam}
	resMember             = tenantResource{"member", ownerTeam}
	resInvitation         = tenantResource{"invitation", ownerTeam}
	resTeamToken          = tenantResource{"team token", ownerTeam}
	resPipeline           = tenantResource{"pipeline", ownerTeam}
	resConcurrencyKey     = tenantResource{"concurrency key", ownerTeam}
	resGitCredential      = tenantResource{"git credential", ownerTeam}
	resGitHubRunner       = tenantResource{"github runner binding", ownerTeam}
	resGitHubInstallation = tenantResource{"github app installation", ownerTeam}
	resOwnInvitation      = tenantResource{"invitation to the caller", ownerAccount}
	resOwnIdentity        = tenantResource{"sign-in identity", ownerAccount}
	resToken              = tenantResource{"token", ownerDeployment}
	resTeam               = tenantResource{"team", ownerDeployment}
	resAccount            = tenantResource{"account", ownerDeployment}
	resAnyInstallation    = tenantResource{"any team's github app installation", ownerDeployment}
	resStorageQuota       = tenantResource{"storage quota", ownerDeployment}
	resPayment            = tenantResource{"payment", ownerDeployment}
	resGitCache           = tenantResource{"git cache repository", ownerDeployment}
)

// safety: teamBoundary refuses a route missing here, and a test fails while
// server.go registers one this table lacks, so a new route is classified
// before it serves anything.
var tenantRoutes = map[string]tenantResource{
	"DELETE /api/v1/accounts/{account}": resAccount,

	"GET /api/v1/admin/usage-metrics": resNone,

	"GET /api/v1/agents": resNone,

	"GET /api/v1/approvals/pending": resNone,

	"GET /api/v1/auth/whoami": resNone,

	"POST /api/v1/cache/store-ceiling/measure": resNone,
	"POST /api/v1/cache/store-ceiling/thaw":    resNone,

	"GET /api/v1/capacity/profiles":         resNone,
	"GET /api/v1/capacity/profiles/explain": resNone,

	"GET /api/v1/compute-limits": resNone,
	"PUT /api/v1/compute-limits": resNone,

	"POST /api/v1/concurrency/{key}/acquire":       resConcurrencyKey,
	"POST /api/v1/concurrency/{key}/cancel-waiter": resConcurrencyKey,
	"POST /api/v1/concurrency/{key}/force-release": resConcurrencyKey,
	"POST /api/v1/concurrency/{key}/heartbeat":     resConcurrencyKey,
	"GET /api/v1/concurrency/{key}/holder":         resConcurrencyKey,
	"POST /api/v1/concurrency/{key}/release":       resConcurrencyKey,
	"GET /api/v1/concurrency/{key}/resolve":        resConcurrencyKey,
	"GET /api/v1/concurrency/{key}/state":          resConcurrencyKey,

	"GET /api/v1/credits":                      resNone,
	"POST /api/v1/credits/card-payments":       resNone,
	"POST /api/v1/credits/card-refunds":        resNone,
	"POST /api/v1/credits/cards":               resNone,
	"POST /api/v1/credits/freezes":             resNone,
	"POST /api/v1/credits/grants":              resNone,
	"GET /api/v1/credits/history":              resNone,
	"GET /api/v1/credits/payments/{reference}": resPayment,
	"POST /api/v1/credits/reversals":           resNone,
	"GET /api/v1/credits/settings":             resNone,
	"PUT /api/v1/credits/settings":             resNone,
	"GET /api/v1/credits/units":                resNone,
	"POST /api/v1/credits/warnings":            resNone,

	"GET /api/v1/crons":                  resNone,
	"GET /api/v1/crons/{id}":             resCron,
	"POST /api/v1/crons/{id}/disarm":     resCron,
	"DELETE /api/v1/crons/{id}/override": resCron,
	"PUT /api/v1/crons/{id}/override":    resCron,
	"POST /api/v1/crons/{id}/pause":      resCron,
	"POST /api/v1/crons/{id}/resume":     resCron,
	"POST /api/v1/crons/{id}/run":        resCron,
	"DELETE /api/v1/crons/repos":         resNone,
	"PUT /api/v1/crons/repos":            resNone,

	"GET /api/v1/gitcache/git/{path...}":  resGitCache,
	"POST /api/v1/gitcache/git/{path...}": resGitCache,
	"POST /api/v1/gitcache/git/register":  resNone,

	"DELETE /api/v1/github-app/installations/{installation_id}": resAnyInstallation,

	"POST /api/v1/invitations/{id}/accept": resOwnInvitation,

	"POST /api/v1/launcher/claim": resNone,
	"POST /api/v1/launcher/sync":  resNone,

	"DELETE /api/v1/me":                       resNone,
	"GET /api/v1/me":                          resNone,
	"POST /api/v1/me/active-team":             resNone,
	"GET /api/v1/me/identities":               resNone,
	"DELETE /api/v1/me/identities/{provider}": resOwnIdentity,
	"GET /api/v1/me/team-deletions":           resNone,

	"POST /api/v1/nodes/claim": resNone,

	"GET /api/v1/object-store/breaker":        resNone,
	"POST /api/v1/object-store/reset-breaker": resNone,

	"GET /api/v1/operator/teams":                  resNone,
	"GET /api/v1/operator/teams/{team}":           resTeam,
	"POST /api/v1/operator/teams/{team}/freeze":   resTeam,
	"POST /api/v1/operator/teams/{team}/grants":   resTeam,
	"POST /api/v1/operator/teams/{team}/trust":    resTeam,
	"POST /api/v1/operator/teams/{team}/unfreeze": resTeam,
	"GET /api/v1/operator/waitlist":               resNone,
	"POST /api/v1/operator/waitlist/approve":      resNone,

	"GET /api/v1/pipelines":                              resNone,
	"GET /api/v1/pipelines/{name}/latest":                resPipeline,
	"GET /api/v1/pipelines/{name}/profile":               resPipeline,
	"POST /api/v1/pipelines/{name}/profile/contention":   resPipeline,
	"POST /api/v1/pipelines/{name}/profile/observations": resPipeline,
	"PUT /api/v1/pipelines/{name}/profile/pin":           resPipeline,
	"POST /api/v1/pipelines/{name}/profile/waits":        resPipeline,

	"GET /api/v1/queue/state": resNone,

	"GET /api/v1/runs":                                               resNone,
	"POST /api/v1/runs":                                              resNone,
	"GET /api/v1/runs/grep":                                          resNone,
	"DELETE /api/v1/runs/{id}":                                       resRun,
	"GET /api/v1/runs/{id}":                                          resRun,
	"GET /api/v1/runs/{id}/approvals":                                resRun,
	"GET /api/v1/runs/{id}/approvals/{nodeID}":                       resRun,
	"POST /api/v1/runs/{id}/approvals/{nodeID}":                      resRun,
	"POST /api/v1/runs/{id}/approvals/{nodeID}/request":              resRun,
	"GET /api/v1/runs/{id}/attempts":                                 resRun,
	"POST /api/v1/runs/{id}/cache-grant":                             resRun,
	"POST /api/v1/runs/{id}/cancel":                                  resRun,
	"POST /api/v1/runs/{id}/children":                                resRun,
	"GET /api/v1/runs/{id}/children/{childID}":                       resRun,
	"GET /api/v1/runs/{id}/children/{childID}/nodes/{nodeID}/output": resRun,
	"GET /api/v1/runs/{id}/debug-pauses":                             resRun,
	"POST /api/v1/runs/{id}/debug-pauses":                            resRun,
	"GET /api/v1/runs/{id}/events":                                   resRun,
	"POST /api/v1/runs/{id}/events":                                  resRun,
	"GET /api/v1/runs/{id}/events/stream":                            resRun,
	"POST /api/v1/runs/{id}/finish":                                  resRun,
	"GET /api/v1/runs/{id}/gitcache/git/{path...}":                   resRun,
	"POST /api/v1/runs/{id}/gitcache/git/{path...}":                  resRun,
	"POST /api/v1/runs/{id}/gitcache/git/register":                   resRun,
	"GET /api/v1/runs/{id}/logs":                                     resRun,
	"GET /api/v1/runs/{id}/logs/{node}":                              resRun,
	"GET /api/v1/runs/{id}/logs/{node}/completeness":                 resRun,
	"GET /api/v1/runs/{id}/logs/{node}/stream":                       resRun,
	"GET /api/v1/runs/{id}/logs/search":                              resRun,
	"GET /api/v1/runs/{id}/nodes":                                    resRun,
	"POST /api/v1/runs/{id}/nodes":                                   resRun,
	"GET /api/v1/runs/{id}/nodes/{nodeID}":                           resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/activity":                 resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/annotations":              resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/attempt":                  resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/claim":                    resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/claim/input":              resRun,
	"GET /api/v1/runs/{id}/nodes/{nodeID}/debug-pause":               resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/execution-finish":         resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/execution-start":          resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/finish":                   resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/heartbeat":                resRun,
	"GET /api/v1/runs/{id}/nodes/{nodeID}/logs":                      resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/logs":                     resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/log-manifest":             resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/log-segments":             resRun,
	"GET /api/v1/runs/{id}/nodes/{nodeID}/logs/stream":               resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/mark-ready":               resRun,
	"GET /api/v1/runs/{id}/nodes/{nodeID}/metrics":                   resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/metrics":                  resRun,
	"GET /api/v1/runs/{id}/nodes/{nodeID}/output":                    resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/output-commit":            resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/output-upload":            resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/release":                  resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/start":                    resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/annotations":        resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/finish":             resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/skip":               resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/start":              resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/steps/summary":            resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/summary":                  resRun,
	"POST /api/v1/runs/{id}/nodes/{nodeID}/usage":                    resRun,
	"POST /api/v1/runs/{id}/oidc-token":                              resRun,
	"GET /api/v1/runs/{id}/paused":                                   resRun,
	"GET /api/v1/runs/{id}/pending-triggers":                         resRun,
	"POST /api/v1/runs/{id}/plan":                                    resRun,
	"GET /api/v1/runs/{id}/receipt":                                  resRun,
	"POST /api/v1/runs/{id}/retry":                                   resRun,
	"POST /api/v1/runs/{id}/source-credential":                       resRun,
	"GET /api/v1/runs/{id}/steps":                                    resRun,

	"GET /api/v1/secrets":           resNone,
	"POST /api/v1/secrets":          resNone,
	"DELETE /api/v1/secrets/{name}": resSecret,
	"GET /api/v1/secrets/{name}":    resSecret,
	"POST /api/v1/secrets/rotate":   resNone,

	"GET /api/v1/services": resNone,

	"GET /api/v1/signups": resNone,
	"PUT /api/v1/signups": resNone,

	"GET /api/v1/storage":                         resNone,
	"PUT /api/v1/storage/quotas/{team}":           resStorageQuota,
	"PUT /api/v1/storage/quotas/{team}/allowance": resStorageQuota,
	"PUT /api/v1/storage/settings":                resNone,

	"DELETE /api/v1/team": resNone,
	"PATCH /api/v1/team":  resNone,

	"GET /api/v1/team/billing":           resNone,
	"PUT /api/v1/team/billing/budget":    resNone,
	"POST /api/v1/team/billing/card":     resNone,
	"POST /api/v1/team/billing/checkout": resNone,
	"POST /api/v1/team/billing/pay":      resNone,

	"GET /api/v1/team/build-trust": resNone,
	"PUT /api/v1/team/build-trust": resNone,

	"GET /api/v1/team/git-credentials":                 resNone,
	"POST /api/v1/team/git-credentials":                resNone,
	"DELETE /api/v1/team/git-credentials/{host}":       resGitCredential,
	"POST /api/v1/team/git-credentials/{host}/confirm": resGitCredential,
	"GET /api/v1/team/git-credentials/releases":        resNone,

	"GET /api/v1/team/github-app":                                              resNone,
	"DELETE /api/v1/team/github-app/automation":                                resNone,
	"GET /api/v1/team/github-app/automation":                                   resNone,
	"PUT /api/v1/team/github-app/automation":                                   resNone,
	"GET /api/v1/team/github-app/extra-repos":                                  resNone,
	"PUT /api/v1/team/github-app/extra-repos":                                  resNone,
	"DELETE /api/v1/team/github-app/installations/{installation_id}":           resGitHubInstallation,
	"GET /api/v1/team/github-app/installations/{installation_id}/repositories": resGitHubInstallation,
	"DELETE /api/v1/team/github-app/triggers":                                  resNone,
	"GET /api/v1/team/github-app/triggers":                                     resNone,
	"PUT /api/v1/team/github-app/triggers":                                     resNone,

	"GET /api/v1/team/github-runners":                    resNone,
	"POST /api/v1/team/github-runners":                   resNone,
	"DELETE /api/v1/team/github-runners/{repository_id}": resGitHubRunner,

	"GET /api/v1/team/invitations":         resNone,
	"POST /api/v1/team/invitations":        resNone,
	"DELETE /api/v1/team/invitations/{id}": resInvitation,

	"GET /api/v1/team/members":              resNone,
	"DELETE /api/v1/team/members/{user_id}": resMember,
	"PATCH /api/v1/team/members/{user_id}":  resMember,

	"POST /api/v1/teams":                                     resNone,
	"DELETE /api/v1/teams/{team}":                            resTeam,
	"PUT /api/v1/teams/{team}/repos/{owner}/{name}/dispatch": resTeam,

	"GET /api/v1/team/tokens":                          resNone,
	"POST /api/v1/team/tokens":                         resNone,
	"DELETE /api/v1/team/tokens/{prefix}":              resTeamToken,
	"PUT /api/v1/team/tokens/{prefix}/git-credentials": resTeamToken,

	"GET /api/v1/tokens":                   resNone,
	"POST /api/v1/tokens":                  resNone,
	"DELETE /api/v1/tokens/{prefix}":       resToken,
	"GET /api/v1/tokens/{prefix}":          resToken,
	"POST /api/v1/tokens/{prefix}/metered": resToken,
	"POST /api/v1/tokens/{prefix}/rotate":  resToken,

	"GET /api/v1/trends": resNone,

	"GET /api/v1/triggers":                 resNone,
	"POST /api/v1/triggers":                resNone,
	"POST /api/v1/triggers/claim":          resNone,
	"GET /api/v1/triggers/{id}":            resTrigger,
	"POST /api/v1/triggers/{id}/claim":     resTrigger,
	"POST /api/v1/triggers/{id}/done":      resTrigger,
	"POST /api/v1/triggers/{id}/heartbeat": resTrigger,
	"GET /api/v1/triggers/spawned-child":   resNone,

	"GET /metrics": resNone,
}
