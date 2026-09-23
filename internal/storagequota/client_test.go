package storagequota_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
)

// fakeController answers the counter routes with whatever its fields say.
type fakeController struct {
	mu     sync.Mutex
	status int
	tier   storagequota.Tier
	calls  []string
	bodies []map[string]any
	down   bool
}

func (f *fakeController) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.URL.Path+" "+r.Header.Get("Authorization"))
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.bodies = append(f.bodies, body)
	if f.down {
		http.Error(w, "database unreachable", http.StatusInternalServerError)
		return
	}
	if f.status != 0 && f.status != http.StatusOK {
		if f.status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "7200")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "refused by the fake"})
		return
	}
	switch r.URL.Path {
	case "/internal/storage/reserve":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"reservation": "sr_1", "tier": f.tier, "granted_bytes": body["bytes"],
		})
	case "/internal/downloads/charge":
		_ = json.NewEncoder(w).Encode(map[string]any{"tier": f.tier, "day_bytes": body["bytes"], "cap_bytes": 100})
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newClient(t *testing.T, f *fakeController) (*storagequota.Client, *httptest.Server, *clock) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := &clock{now: time.Now()}
	return storagequota.New(srv.URL, nil).WithClock(c.Now), srv, c
}

func TestReserveCommitAndReleaseCarryTheCallersBearer(t *testing.T) {
	f := &fakeController{tier: storagequota.TierFree}
	c, _, _ := newClient(t, f)
	ctx := context.Background()
	res, err := c.Reserve(ctx, "Bearer tok", "team-a", storagequota.KindCache, 10, false)
	if err != nil || res.ID != "sr_1" || res.Granted != 10 || res.Tier != storagequota.TierFree {
		t.Fatalf("reserve = %+v, %v", res, err)
	}
	if err := c.Commit(ctx, "Bearer tok", res, 8); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := c.Release(ctx, "Bearer tok", res); err != nil {
		t.Fatalf("release: %v", err)
	}
	want := []string{
		"/internal/storage/reserve Bearer tok", "/internal/storage/commit Bearer tok", "/internal/storage/release Bearer tok",
	}
	for i, w := range want {
		if f.calls[i] != w {
			t.Fatalf("call %d = %q, want %q", i, f.calls[i], w)
		}
	}
	if f.bodies[1]["bytes"] != float64(8) || f.bodies[1]["team"] != "team-a" || f.bodies[1]["store"] != "cache" {
		t.Fatalf("commit body = %v", f.bodies[1])
	}
}

func TestRefusalsKeepTheirMeaning(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		status int
		check  func(error) bool
	}{
		{http.StatusRequestEntityTooLarge, func(err error) bool {
			var q *storagequota.QuotaError
			return errors.As(err, &q) && !q.Paused
		}},
		{http.StatusPaymentRequired, func(err error) bool {
			var q *storagequota.QuotaError
			return errors.As(err, &q) && q.Paused
		}},
	} {
		c, _, _ := newClient(t, &fakeController{status: tc.status})
		if _, err := c.Reserve(ctx, "Bearer tok", "team-a", storagequota.KindCache, 10, false); !tc.check(err) {
			t.Errorf("reserve answered %d = %v", tc.status, err)
		}
	}
	c, _, _ := newClient(t, &fakeController{status: http.StatusTooManyRequests})
	err := c.ChargeDownload(ctx, "Bearer tok", "team-a", 10, false)
	var capErr *storagequota.DownloadCapError
	if !errors.As(err, &capErr) || capErr.RetryAfter != 2*time.Hour {
		t.Fatalf("charge answered 429 = %v, want a cap error retrying in 2h", err)
	}
}

// A controller that cannot answer is unavailable to a free team, which the
// caller refuses with 503. A funded answer lets that team through for five
// minutes, and not after.
func TestAnUnreachableControllerFailsClosedUnlessTheTeamWasFundedRecently(t *testing.T) {
	ctx := context.Background()
	f := &fakeController{tier: storagequota.TierFunded}
	c, srv, clk := newClient(t, f)
	if _, err := c.Reserve(ctx, "Bearer tok", "team-free", storagequota.KindCache, 10, false); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.tier = storagequota.TierFree
	f.mu.Unlock()
	if _, err := c.Reserve(ctx, "Bearer tok", "team-once-free", storagequota.KindCache, 10, false); err != nil {
		t.Fatal(err)
	}
	srv.Close()

	res, err := c.Reserve(ctx, "Bearer tok", "team-free", storagequota.KindCache, 10, false)
	if err != nil || res.Tier != storagequota.TierFunded || !res.Unlimited {
		t.Fatalf("a team funded a moment ago with the controller down = %+v, %v; want let through", res, err)
	}
	if err := c.ChargeDownload(ctx, "Bearer tok", "team-free", 10, false); err != nil {
		t.Fatalf("a funded team's download with the controller down: %v", err)
	}
	if _, err := c.Reserve(ctx, "Bearer tok", "team-once-free", storagequota.KindCache, 10, false); !errors.Is(err, storagequota.ErrUnavailable) {
		t.Fatalf("a free team with the controller down = %v, want unavailable", err)
	}
	if err := c.ChargeDownload(ctx, "Bearer tok", "team-never-seen", 10, false); !errors.Is(err, storagequota.ErrUnavailable) {
		t.Fatalf("a team never answered for with the controller down = %v, want unavailable", err)
	}
	clk.now = clk.now.Add(storagequota.MaxFundedAge + time.Second)
	if _, err := c.Reserve(ctx, "Bearer tok", "team-free", storagequota.KindCache, 10, false); !errors.Is(err, storagequota.ErrUnavailable) {
		t.Fatalf("a funded answer older than %s with the controller down = %v, want unavailable", storagequota.MaxFundedAge, err)
	}
}

func TestAControllerErrorIsUnavailable(t *testing.T) {
	c, _, _ := newClient(t, &fakeController{down: true})
	if _, err := c.Reserve(context.Background(), "Bearer tok", "team-a", storagequota.KindLogs, 10, false); !errors.Is(err, storagequota.ErrUnavailable) {
		t.Fatalf("a 500 from the controller = %v, want unavailable", err)
	}
}
