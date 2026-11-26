package capability

import (
	"net/http"
	"testing"
)

func TestLeaseBoundAdmitsOnlyLiveClaimAndEvalRules(t *testing.T) {
	for _, path := range []string{
		"/api/v1/claims/{claim}/heartbeat",
		"/api/v1/runs/{run}/heartbeat",
		"/api/v1/runs/{run}/jobs/{job}/heartbeat",
		"/api/v1/runs/{run}/jobs/{job}/transition",
		"/api/v1/runs/{run}/local-host/nodes/{job}/transition",
		"/api/v1/runs/{run}/jobs/{job}/steps/start",
		"/api/v1/runs/{run}/jobs/{job}/steps/finish",
	} {
		route, ok := LookupRoute(http.MethodPost, path)
		if !ok {
			t.Fatalf("no route %s", path)
		}
		for _, rule := range route.Access {
			want := rule.Credential == CredentialClaim || rule.Credential == credentialEval
			if got := (Authorized{Access: rule}).LeaseBound(); got != want {
				t.Errorf("%s %s/%s: lease-bound=%v, want %v", path, rule.Credential, rule.Liveness, got, want)
			}
		}
	}
	for name, a := range map[string]Authorized{
		"publisher with result liveness": {Access: AccessRule{Credential: CredentialPublisher, Liveness: LiveResult}},
		"member with reporting liveness": {Access: AccessRule{Credential: CredentialMember, Liveness: LiveReporting}},
		"claim with credential liveness": {Access: AccessRule{Credential: CredentialClaim, Liveness: LiveCredential}},
		"claim result replay":            {Access: AccessRule{Credential: CredentialClaim, Liveness: LiveResult}, ResultReplay: true},
		"claim log tail":                 {Access: AccessRule{Credential: CredentialClaim, Liveness: liveLogTail}, LogTailAttempt: 1},
		"eval log tail":                  {Access: AccessRule{Credential: credentialEval, Liveness: liveLogTail}},
	} {
		if a.LeaseBound() {
			t.Errorf("%s counted as lease-bound", name)
		}
	}
}
