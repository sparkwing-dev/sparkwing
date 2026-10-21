package controller

import (
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestPersistentRunnerCannotReportLauncherNamespace(t *testing.T) {
	_, handler, root := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	engineClaimHTTP(t, handler, root, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"],"job_namespace":"cloud","available_cores":4}`, http.StatusBadRequest)
}
