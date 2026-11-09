package controller_test

import (
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestConsoleTriggerKeepsRepositoryRequirements(t *testing.T) {
	f := newDeploymentAppFixture(t, deploymenttest.Cloud(t, "https://controller.test"))
	owner := f.ghUser(501, "olga")
	f.connect(owner, 501, 7, acmeAdmin)
	config := []byte("pipelines:\n  - name: deploy\n    entrypoint: Deploy\n    requires: [deploy-kikd-prod]\n  - name: deploy-scan\n    entrypoint: DeployScan\n    requires: [deploy-kikd-prod]\n")
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", config)
	for _, pipeline := range []string{"deploy", "deploy-scan"} {
		for _, supplied := range []string{"repository", "caller", "empty"} {
			t.Run(fmt.Sprintf("%s/%s", pipeline, supplied), func(t *testing.T) {
				request := controllerwire.CreateRunRequest{IdempotencyKey: fmt.Sprintf("console-%s-%s", pipeline, supplied), Request: runspec.Request{
					Version: 1, Pipeline: pipeline, Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost,
					Git:    runspec.Git{Repository: runspec.Repository{Key: "gh:701", Display: "acme/widgets"}, Ref: "main"},
					Source: runspec.Source{Ref: "main", Location: runspec.SourceLocation{RemoteURL: "https://github.com/acme/widgets", PipelineDir: ".sparkwing"}},
				}}
				expected := []string{"deploy-kikd-prod"}
				if supplied != "repository" {
					expected = []string{"caller-runner"}
					if supplied == "empty" {
						expected = nil
					}
					request.Request.Options.Requires = runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: expected}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "requires"}}
				}
				var created controllerwire.CreateRunResponse
				if code := f.call("POST", "/api/v1/runs", owner.auth, request, &created); code != http.StatusCreated {
					t.Fatalf("console trigger = %d", code)
				}
				sealed := f.sealedRequest(created.RunID)
				t.Logf("pipeline=%s sha=%s requires=%v", pipeline, sealed.Git.SHA, sealed.Options.Requires.Value)
				machine, _ := f.teamRunner(owner, fmt.Sprintf("unlabelled-%s-%s", pipeline, supplied))
				var claim controllerwire.ClaimResponse
				code := f.call("POST", "/api/v1/runners/claim", machine, githubClaimBody(capability.UnitPlan, created.RunID, ""), &claim)
				wantCode := http.StatusForbidden
				if supplied == "empty" {
					wantCode = http.StatusOK
				}
				if code != wantCode || (claim.Claim != nil) != (supplied == "empty") {
					t.Errorf("unlabelled runner claimed %s planning: HTTP %d accepted=%t", pipeline, code, claim.Claim != nil)
				}
				if sealed.Git.SHA != headSHA || !sealed.Options.Requires.Set || !slices.Equal(sealed.Options.Requires.Value, expected) {
					t.Errorf("sealed sha=%s requires=%v; want %s %v", sealed.Git.SHA, sealed.Options.Requires, headSHA, expected)
				}
			})
		}
	}
}
