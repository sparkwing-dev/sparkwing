package controller_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// An unknown role names itself and the roles that exist, because "member" is a
// natural guess and a bare "invalid request" leaves the caller to find the list.
func TestUnknownRoleRefusalNamesTheRoleAndTheValidOnes(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		carol, err := f.st.AccountByEmail(context.Background(), "carol@example.test")
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			method, path string
			body         map[string]string
		}{
			{http.MethodPost, "/api/v1/team/invitations", map[string]string{"email": "erin@example.test", "role": "member"}},
			{http.MethodPatch, "/api/v1/team/members/" + carol.ID, map[string]string{"role": "member"}},
		} {
			code, raw := f.do(c.method, c.path, f.ownerA, c.body)
			if code != http.StatusBadRequest {
				t.Fatalf("%s %s = %d %s, want 400", c.method, c.path, code, raw)
			}
			for _, want := range []string{`\"member\"`, "owner, editor, or reader"} {
				if !strings.Contains(raw, want) {
					t.Errorf("%s %s = %s, want it to contain %s", c.method, c.path, raw, want)
				}
			}
		}
	})
}
