package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

// repoClaimController serves claims the way a controller does: it decodes the
// body strictly, so a controller that predates allow_repos answers 400.
type repoClaimController struct {
	mu         sync.Mutex
	caps       string
	knowsField bool
	sent       [][]string
}

func (c *repoClaimController) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v1/capabilities":
		if c.caps == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(c.caps))
	case "/api/v1/triggers/claim", "/api/v1/nodes/claim":
		var body map[string]json.RawMessage
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		raw, has := body["allow_repos"]
		if has && !c.knowsField {
			http.Error(w, `{"error":"json: unknown field \"allow_repos\""}`, http.StatusBadRequest)
			return
		}
		var sent []string
		if has {
			_ = json.Unmarshal(raw, &sent)
		}
		c.mu.Lock()
		c.sent = append(c.sent, sent)
		c.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func claimBoth(t *testing.T, c *client.Client) {
	t.Helper()
	ctx := context.Background()
	if _, err := c.ClaimTrigger(ctx); err != nil {
		t.Fatalf("trigger claim: %v", err)
	}
	if _, err := c.ClaimNode(ctx, "runner:laptop:1", nil, time.Minute, nil); err != nil {
		t.Fatalf("node claim: %v", err)
	}
}

// A runner and its controller upgrade independently, so a new runner against
// a controller that does not advertise the filter claims without the field and
// relies on refusing a disallowed run itself.
func TestAllowReposIsSentOnlyToAControllerThatAdvertisesIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps string
	}{
		{"capabilities without the claim filter", `{"teams":{"enabled":false},"auth":{"providers":[]}}`},
		{"no capabilities route", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := &repoClaimController{caps: tc.caps}
			srv := httptest.NewServer(ctrl)
			defer srv.Close()
			claimBoth(t, client.New(srv.URL, nil).WithAllowRepos([]string{"github.com/acme/*"}))
			for _, sent := range ctrl.sent {
				if sent != nil {
					t.Fatalf("sent allow_repos %q to a controller that does not advertise it", sent)
				}
			}
		})
	}

	ctrl := &repoClaimController{knowsField: true, caps: `{"claims":{"allow_repos":true}}`}
	srv := httptest.NewServer(ctrl)
	defer srv.Close()
	claimBoth(t, client.New(srv.URL, nil).WithAllowRepos([]string{"github.com/acme/*"}))
	if len(ctrl.sent) != 2 {
		t.Fatalf("claims = %d, want 2", len(ctrl.sent))
	}
	for _, sent := range ctrl.sent {
		if len(sent) != 1 || sent[0] != "github.com/acme/*" {
			t.Fatalf("sent allow_repos %q to a controller that advertises it, want the list", sent)
		}
	}
}
