package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/executorinfo"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

type profileClaimController struct {
	caps   string
	mu     sync.Mutex
	bodies []map[string]json.RawMessage
}

func (c *profileClaimController) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v1/capabilities":
		_, _ = w.Write([]byte(c.caps))
	case "/api/v1/triggers/claim", "/api/v1/nodes/claim":
		body := map[string]json.RawMessage{}
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		c.mu.Lock()
		c.bodies = append(c.bodies, body)
		c.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

// A runner reports the platform it runs on with every claim without being
// configured to, and only to a controller that reads the field.
func TestClaimsAdvertiseThePlatformWithoutConfiguration(t *testing.T) {
	want := executorinfo.DetectObservedPlatform()
	for _, tc := range []struct {
		name string
		caps string
		sent bool
	}{
		{"controller reads profiles", `{"claims":{"allow_repos":true,"profile":true}}`, true},
		{"controller predates profiles", `{"claims":{"allow_repos":true}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := &profileClaimController{caps: tc.caps}
			srv := httptest.NewServer(ctrl)
			defer srv.Close()
			c := client.New(srv.URL, nil)
			if _, err := c.ClaimTrigger(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := c.ClaimNode(context.Background(), "runner:pi:1", nil, time.Minute, nil); err != nil {
				t.Fatal(err)
			}
			if len(ctrl.bodies) != 2 {
				t.Fatalf("claims seen = %d, want 2", len(ctrl.bodies))
			}
			for _, body := range ctrl.bodies {
				raw, has := body["platform"]
				if has != tc.sent {
					t.Fatalf("platform sent = %v, want %v", has, tc.sent)
				}
				if !has {
					continue
				}
				var got struct{ OS, Arch string }
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatal(err)
				}
				if got.OS != want.OS || got.Arch != want.Arch || got.OS == "" || got.Arch == "" {
					t.Fatalf("platform = %+v, want %s/%s", got, want.OS, want.Arch)
				}
			}
		})
	}
}
