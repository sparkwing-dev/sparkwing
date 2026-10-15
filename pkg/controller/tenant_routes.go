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
	resGitHubRunner       = tenantResource{"github runner binding", ownerTeam}
	resGitHubInstallation = tenantResource{"github app installation", ownerTeam}
	resOwnInvitation      = tenantResource{"invitation to the caller", ownerAccount}
	resOwnIdentity        = tenantResource{"sign-in identity", ownerAccount}
	resToken              = tenantResource{"token", ownerDeployment}
	resTeam               = tenantResource{"team", ownerDeployment}
	resPayment            = tenantResource{"payment", ownerDeployment}
)

// safety: teamBoundary refuses a route missing here, and a test fails while
// server.go registers one this table lacks, so a new route is classified
// before it serves anything.
var tenantRoutes = map[string]tenantResource{
	"DELETE /api/v1/runs/{id}": resRun,

	"DELETE /api/v1/team": resNone,
	"PATCH /api/v1/team":  resNone,

	"PUT /api/v1/team/billing/budget":    resNone,
	"POST /api/v1/team/billing/card":     resNone,
	"POST /api/v1/team/billing/checkout": resNone,
	"POST /api/v1/team/billing/pay":      resNone,

	"PUT /api/v1/team/build-trust": resNone,

	"DELETE /api/v1/team/github-app/automation":                      resNone,
	"GET /api/v1/team/github-app/automation":                         resNone,
	"PUT /api/v1/team/github-app/automation":                         resNone,
	"PUT /api/v1/team/github-app/extra-repos":                        resNone,
	"DELETE /api/v1/team/github-app/installations/{installation_id}": resGitHubInstallation,
	"DELETE /api/v1/team/github-app/triggers":                        resNone,
	"PUT /api/v1/team/github-app/triggers":                           resNone,

	"POST /api/v1/team/invitations":        resNone,
	"DELETE /api/v1/team/invitations/{id}": resInvitation,
}
