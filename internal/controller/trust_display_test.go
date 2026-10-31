package controller_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestRunListsDisplaySealedTrustClass(t *testing.T) {
	f := newBrowserFixture(t, nil, fixtureStorePolicies(store.Policies{Intake: receiptPolicy{}}))
	admitted := secretArgRun(t, f, ctlSecretValue, true)
	for _, accept := range []string{"", "application/json"} {
		for _, path := range []string{"/api/v1/runs", "/api/v1/runs/" + admitted.RunID} {
			r := f.cookieRequest(http.MethodGet, path, "")
			r.Header.Set("Accept", accept)
			w := f.serve(r)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"trust_class":"asserted"`) {
				t.Fatalf("GET %s Accept %q = %d %s; want asserted despite main ref", path, accept, w.Code, w.Body)
			}
		}
	}
}
