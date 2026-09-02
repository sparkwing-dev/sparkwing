package controller_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func timeNowUTC() time.Time { return time.Now().UTC() }

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

func TestBootstrap_PostBootstrapRequiresAuth(t *testing.T) {
	base, st, cleanup := newAuthedTestServer(t)
	defer cleanup()

	if _, err := st.CreateUser("preexisting", "correctbatteryhorse", []string{controller.ScopeAdmin}, timeNowUTC()); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	if getBootstrapNeeded(t, base) {
		t.Fatalf("expected needed=false when a user already exists")
	}

	status, _ := postJSONWithStatus(t, base+"/api/v1/users", map[string]string{
		"name":     "usurper",
		"password": "anotherlongone",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthed post-bootstrap POST /users status=%d want 401", status)
	}
}

func TestBootstrap_AuthEnabledRequiresAdminForFirstUser(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	token, _, err := st.CreateToken("test-admin", store.TokenKindUser,
		[]string{controller.ScopeAdmin}, 0, timeNowUTC())
	if err != nil {
		t.Fatalf("seed admin token: %v", err)
	}
	nonAdminToken, _, err := st.CreateToken("reader", store.TokenKindUser,
		[]string{controller.ScopeRunsRead}, 0, timeNowUTC())
	if err != nil {
		t.Fatalf("seed non-admin token: %v", err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	srv := httptest.NewServer(controller.New(st, logger).EnableAuthFromStore().Handler())
	defer srv.Close()

	if getBootstrapNeeded(t, srv.URL) {
		t.Fatal("bootstrap probe reported needed=true while authentication is enabled")
	}

	requestBody := map[string]string{
		"name":     "admin",
		"password": "correctbatteryhorse",
	}
	status, body := postJSONWithStatus(t, srv.URL+"/api/v1/users", requestBody)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated first-user create status=%d body=%s; want 401", status, body)
	}
	users, err := st.ListUsers()
	if err != nil {
		t.Fatalf("list users after rejected create: %v", err)
	}
	if len(users) != 0 {
		t.Fatalf("unauthenticated first-user create inserted %d users; want 0", len(users))
	}

	status, body = postJSONWithBearer(t, srv.URL+"/api/v1/users", nonAdminToken, requestBody)
	if status != http.StatusForbidden {
		t.Fatalf("non-admin first-user create status=%d body=%s; want 403", status, body)
	}

	status, body = postJSONWithBearer(t, srv.URL+"/api/v1/users", token, requestBody)
	if status != http.StatusCreated {
		t.Fatalf("authenticated first-user create status=%d body=%s; want 201", status, body)
	}
	if strings.Contains(logs.String(), "authentication is disabled") {
		t.Fatalf("authenticated first-user create logged disabled authentication: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "authenticated principal") {
		t.Fatalf("authenticated first-user create did not identify its authorization path: %s", logs.String())
	}
}

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

func postJSONWithBearer(t *testing.T, url, token string, body any) (int, string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("create POST %s: %v", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
