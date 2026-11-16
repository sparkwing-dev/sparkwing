package controller_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestSubscriptionLifecycleOverHTTP(t *testing.T) {
	f := newCredFixture(t)
	var created controllerwire.SubscriptionSecret
	body := map[string]any{"url": "https://hooks.example.com/sparkwing?token=abc", "event_types": []string{"run.finished"}, "statuses": []string{"failed"}, "pipelines": []string{"test"}, "description": "pager"}
	if code := f.callKeyed("POST", "/api/v1/subscriptions", f.olga.auth, "create-1", body, &created); code != http.StatusOK {
		t.Fatalf("create = %d", code)
	}
	sub := created.Subscription
	if !strings.HasPrefix(sub.ID, "sub_") || !strings.HasPrefix(created.Secret, "whsec_") || created.Replayed || sub.CreatedBy != f.olga.id || sub.Repositories == nil || len(sub.Statuses) != 1 {
		t.Fatalf("created = %+v", created)
	}
	var replay controllerwire.SubscriptionSecret
	if code := f.callKeyed("POST", "/api/v1/subscriptions", f.olga.auth, "create-1", body, &replay); code != http.StatusOK || !replay.Replayed || replay.Secret != created.Secret || replay.Subscription.ID != sub.ID {
		t.Fatalf("replay = %d %+v", code, replay)
	}
	changed := map[string]any{"url": "https://hooks.example.com/other"}
	if code := f.callKeyed("POST", "/api/v1/subscriptions", f.olga.auth, "create-1", changed, nil); code != http.StatusConflict {
		t.Fatalf("reused key with another body = %d", code)
	}
	var raw json.RawMessage
	if code := f.call("GET", "/api/v1/subscriptions", f.olga.auth, nil, &raw); code != http.StatusOK || strings.Contains(string(raw), "whsec_") || !strings.Contains(string(raw), sub.ID) {
		t.Fatalf("list = %d %s", code, raw)
	}
	if code := f.call("GET", "/api/v1/subscriptions/"+sub.ID, f.olga.auth, nil, &raw); code != http.StatusOK || strings.Contains(string(raw), "whsec_") {
		t.Fatalf("get = %d %s", code, raw)
	}
	var rotated controllerwire.SubscriptionSecret
	if code := f.call("POST", "/api/v1/subscriptions/"+sub.ID+"/rotate-secret", f.olga.auth, nil, &rotated); code != http.StatusOK || rotated.Secret == created.Secret || !strings.HasPrefix(rotated.Secret, "whsec_") {
		t.Fatalf("rotate = %d %+v", code, rotated)
	}
	if code := f.callKeyed("POST", "/api/v1/subscriptions", f.olga.auth, "create-1", body, &replay); code != http.StatusOK || replay.Secret != rotated.Secret {
		t.Fatalf("replay after rotation returned %q, want the current secret", replay.Secret)
	}
	var deleted controllerwire.SubscriptionDeletion
	if code := f.call("DELETE", "/api/v1/subscriptions/"+sub.ID, f.olga.auth, nil, &deleted); code != http.StatusOK || !deleted.Deleted {
		t.Fatalf("delete = %d %+v", code, deleted)
	}
	if code := f.call("DELETE", "/api/v1/subscriptions/"+sub.ID, f.olga.auth, nil, &deleted); code != http.StatusOK || deleted.Deleted {
		t.Fatalf("repeated delete = %d %+v", code, deleted)
	}
	for _, path := range []string{"/api/v1/subscriptions/" + sub.ID, "/api/v1/subscriptions/" + sub.ID + "/rotate-secret"} {
		method := "GET"
		if strings.HasSuffix(path, "rotate-secret") {
			method = "POST"
		}
		if code := f.call(method, path, f.olga.auth, nil, nil); code != http.StatusNotFound {
			t.Fatalf("%s %s after delete = %d", method, path, code)
		}
	}
}

func TestSubscriptionRoutesRequireTeamAdministration(t *testing.T) {
	f := newCredFixture(t)
	eddie := f.ghUser(502, "eddie-gh")
	f.join(f.olga, eddie, "eddie-gh@example.com", "editor")
	var created controllerwire.SubscriptionSecret
	if code := f.call("POST", "/api/v1/subscriptions", f.olga.auth, map[string]any{"url": "https://hooks.example.com/a"}, &created); code != http.StatusOK {
		t.Fatalf("create = %d", code)
	}
	id := created.Subscription.ID
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/subscriptions"},
		{"POST", "/api/v1/subscriptions"},
		{"GET", "/api/v1/subscriptions/" + id},
		{"DELETE", "/api/v1/subscriptions/" + id},
		{"POST", "/api/v1/subscriptions/" + id + "/rotate-secret"},
	} {
		var body any
		if c.method == "POST" && c.path == "/api/v1/subscriptions" {
			body = map[string]any{"url": "https://hooks.example.com/b"}
		}
		if code := f.call(c.method, c.path, eddie.auth, body, nil); code != http.StatusForbidden {
			t.Errorf("editor %s %s = %d, want 403", c.method, c.path, code)
		}
	}
	other := f.ghUser(503, "otto-gh")
	if code := f.call("GET", "/api/v1/subscriptions/"+id, other.auth, nil, nil); code != http.StatusNotFound {
		t.Errorf("another team's owner reads the subscription: %d", code)
	}
	if code := f.call("DELETE", "/api/v1/subscriptions/"+id, other.auth, nil, nil); code != http.StatusOK {
		t.Errorf("another team's delete = %d", code)
	}
	if code := f.call("GET", "/api/v1/subscriptions/"+id, f.olga.auth, nil, nil); code != http.StatusOK {
		t.Errorf("another team's delete reached this team: %d", code)
	}
}

func TestControllerSubscriptionsRefuseNonPublicDestinations(t *testing.T) {
	f := newCredFixture(t)
	for _, url := range []string{
		"http://hooks.example.com/a", "https://localhost/a", "https://127.0.0.1/a", "https://10.0.0.1/a",
		"https://169.254.169.254/latest", "https://[::ffff:127.0.0.1]/a", "https://[::1]:8443/a", "https://hooks.internal/a",
		"https://user:pass@hooks.example.com/a", "https://hooks.example.com/a#frag", "ftp://hooks.example.com/a", "hooks.example.com/a",
		"https://hooks.example.com:99999/a", "",
	} {
		if code := f.call("POST", "/api/v1/subscriptions", f.olga.auth, map[string]any{"url": url}, nil); code != http.StatusBadRequest {
			t.Errorf("create %q = %d, want 400", url, code)
		}
	}
	for _, body := range []map[string]any{
		{"url": "https://hooks.example.com/a", "event_types": []string{"run.succeeded"}},
		{"url": "https://hooks.example.com/a", "event_types": []string{"run.started"}, "statuses": []string{"failed"}},
		{"url": "https://hooks.example.com/a", "statuses": []string{"running"}},
		{"url": "https://hooks.example.com/a", "headers": map[string]string{"X": "y"}},
		{"url": "https://hooks.example.com/a", "description": strings.Repeat("x", 201)},
	} {
		if code := f.call("POST", "/api/v1/subscriptions", f.olga.auth, body, nil); code != http.StatusBadRequest {
			t.Errorf("create %v = %d, want 400", body, code)
		}
	}
	for i := range 20 {
		if code := f.call("POST", "/api/v1/subscriptions", f.olga.auth, map[string]any{"url": "https://hooks.example.com/a"}, nil); code != http.StatusOK {
			t.Fatalf("subscription %d = %d", i, code)
		}
	}
	if code := f.call("POST", "/api/v1/subscriptions", f.olga.auth, map[string]any{"url": "https://hooks.example.com/a"}, nil); code != http.StatusConflict {
		t.Fatalf("subscription past the cap = %d", code)
	}
}

func TestSubscriptionEventTypesDefaultToFinishedWhenStatusesAreGiven(t *testing.T) {
	f := newCredFixture(t)
	for _, c := range []struct {
		body map[string]any
		want []string
	}{
		{map[string]any{"url": "https://hooks.example.com/a", "statuses": []string{"failed"}}, []string{"run.finished"}},
		{map[string]any{"url": "https://hooks.example.com/a"}, []string{"run.started", "run.finished"}},
		{map[string]any{"url": "https://hooks.example.com/a", "event_types": []string{"run.started", "run.finished"}, "statuses": []string{"failed"}}, []string{"run.started", "run.finished"}},
	} {
		var created controllerwire.SubscriptionSecret
		if code := f.call("POST", "/api/v1/subscriptions", f.olga.auth, c.body, &created); code != http.StatusOK || strings.Join(created.Subscription.EventTypes, ",") != strings.Join(c.want, ",") {
			t.Errorf("create %v = %d, event types %v, want %v", c.body, code, created.Subscription.EventTypes, c.want)
		}
	}
}

func TestSubscriptionDescriptionLimitCountsCharacters(t *testing.T) {
	f := newCredFixture(t)
	if code := f.call("POST", "/api/v1/subscriptions", f.olga.auth, map[string]any{"url": "https://hooks.example.com/a", "description": strings.Repeat("é", 200)}, nil); code != http.StatusOK {
		t.Fatalf("200 two-byte characters = %d, want 200", code)
	}
	if code := f.call("POST", "/api/v1/subscriptions", f.olga.auth, map[string]any{"url": "https://hooks.example.com/a", "description": strings.Repeat("é", 201)}, nil); code != http.StatusBadRequest {
		t.Fatalf("201 characters = %d, want 400", code)
	}
}

func TestSubscriptionCapNamesTheCommandsThatFreeASlot(t *testing.T) {
	f := newCredFixture(t)
	for range 20 {
		if code := f.call("POST", "/api/v1/subscriptions", f.olga.auth, map[string]any{"url": "https://hooks.example.com/a"}, nil); code != http.StatusOK {
			t.Fatalf("create = %d", code)
		}
	}
	var refusal struct {
		Error struct {
			Code string `json:"code"`
			Hint string `json:"hint"`
		} `json:"error"`
	}
	if code := f.call("POST", "/api/v1/subscriptions", f.olga.auth, map[string]any{"url": "https://hooks.example.com/a"}, &refusal); code != http.StatusConflict ||
		!strings.Contains(refusal.Error.Hint, "sparkwing subscriptions list") || !strings.Contains(refusal.Error.Hint, "sparkwing subscriptions delete --subscription") {
		t.Fatalf("cap refusal = %d %+v", code, refusal)
	}
}
