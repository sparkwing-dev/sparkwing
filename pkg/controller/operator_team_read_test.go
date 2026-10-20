package controller

import (
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestOperatorTeamReadRequiresCurrentDeploymentGrantProperty(t *testing.T) {
	clock := time.Now().UTC()
	e, _ := engineRuntimeStore(t, teststore.WithClock(func() time.Time { return clock }))
	raw := reviewSeed(t, e.Store)
	parent, err := e.Store.LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	h := New(e.Store, nil).Handler()
	property := func(granted, expired, forged bool, adversarial string) bool {
		grants := []capability.Grant{capability.OperatorLimits}
		if granted {
			grants = append(grants, capability.OperatorTokens)
		}
		expires := clock.Add(time.Minute)
		issued, err := e.Store.DelegateCapability(t.Context(), capability.DelegateSpec{Parent: parent.Authority, Audience: capability.Controller, Grants: grants, Binding: capability.Binding{Kind: capability.BindingNone}, ExpiresAt: &expires})
		if err != nil {
			t.Fatal(err)
		}
		if expired {
			clock = expires
		}
		token := issued.Raw
		if forged {
			token = "swk_" + strings.Repeat("Z", 43)
		}
		r := httptest.NewRequest("GET", "https://controller.test/api/v1/operator/teams/default?team="+url.QueryEscape(adversarial), nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Operator", url.QueryEscape(adversarial))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if !granted || expired || forged {
			return w.Code == 401 || w.Code == 403
		}
		var fields map[string]any
		return w.Code == 200 && json.Unmarshal(w.Body.Bytes(), &fields) == nil && len(fields) == 2 && fields["team"] == "default" && fields["display_name"] != nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 60}); err != nil {
		t.Fatal(err)
	}
}
