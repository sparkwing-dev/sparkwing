package local_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func timeNowUTC() time.Time { return time.Now().UTC() }

// TestBootstrap_NeededOnEmpty asserts the unauthenticated probe
// returns true on a fresh store and false once a user exists, so the
// web pod's /login render flips from the signup form to the normal
// login form without a controller restart.
func TestBootstrap_NeededOnEmpty(t *testing.T) {
	base, _, cleanup := newTestServer(t)
	defer cleanup()

	if !getBootstrapNeeded(t, base) {
		t.Fatalf("expected needed=true on empty users table")
	}

	status, body := postJSONWithStatus(t, base+"/api/v1/users", map[string]string{
		"name":     "admin",
		"password": "correctbatteryhorse",
	})
	if status != http.StatusCreated {
		t.Fatalf("bootstrap create status=%d body=%s", status, body)
	}

	if getBootstrapNeeded(t, base) {
		t.Fatalf("expected needed=false once a user exists")
	}
}

// TestBootstrap_PostBootstrapRequiresAuth covers the post-bootstrap
// safety requirement: once a user exists AND auth is on, POST
// /api/v1/users reverts to admin-scoped and an unauthenticated call
// is rejected. No reopening of the bootstrap path.
func TestBootstrap_PostBootstrapRequiresAuth(t *testing.T) {
	base, st, cleanup := newAuthedTestServer(t)
	defer cleanup()

	// Seed a user so bootstrap is definitively closed.
	if _, err := st.CreateUser("preexisting", "correctbatteryhorse", timeNowUTC()); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	if getBootstrapNeeded(t, base) {
		t.Fatalf("expected needed=false when a user already exists")
	}

	// Unauthenticated second POST must 401 (auth middleware kicks in).
	status, _ := postJSONWithStatus(t, base+"/api/v1/users", map[string]string{
		"name":     "usurper",
		"password": "anotherlongone",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthed post-bootstrap POST /users status=%d want 401", status)
	}
}

// TestBootstrap_ConcurrentSignupRace fires many parallel unauthed
// POSTs against a fresh controller and asserts exactly one wins. The
// emptiness check happens inside the insert tx (store.CreateFirstUser)
// so the race is resolved deterministically.
func TestBootstrap_ConcurrentSignupRace(t *testing.T) {
	base, st, cleanup := newTestServer(t)
	defer cleanup()

	const N = 16
	var wg sync.WaitGroup
	var created int32
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			status, _ := postJSONWithStatus(t, base+"/api/v1/users", map[string]string{
				"name":     "admin",
				"password": "correctbatteryhorse",
			})
			if status == http.StatusCreated {
				atomic.AddInt32(&created, 1)
			}
		}()
	}
	wg.Wait()

	if created != 1 {
		t.Fatalf("expected exactly 1 successful bootstrap, got %d", created)
	}

	users, err := st.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("expected 1 user row, got %d", len(users))
	}
}

// --- helpers ---

func getBootstrapNeeded(t *testing.T, base string) bool {
	t.Helper()
	resp, err := http.Get(base + "/api/v1/auth/bootstrap-needed")
	if err != nil {
		t.Fatalf("get bootstrap-needed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap-needed status=%d", resp.StatusCode)
	}
	var body struct {
		Needed bool `json:"needed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Needed
}

func postJSONWithStatus(t *testing.T, url string, body any) (int, string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
