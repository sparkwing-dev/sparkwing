package controller_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func userScopesServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := controller.New(st, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postUserJSON(t *testing.T, url string, body any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func bootstrapAdmin(t *testing.T, ts *httptest.Server) {
	t.Helper()
	status, body := postUserJSON(t, ts.URL+"/api/v1/users", map[string]any{
		"name": "root", "password": "correct-horse",
	})
	if status != http.StatusCreated {
		t.Fatalf("bootstrap admin = %d: %s", status, body)
	}
}

func TestCreateUser_ScopesFlowIntoTheSession(t *testing.T) {
	for _, test := range []struct {
		name    string
		request map[string]any
		want    []string
	}{
		{
			name:    "omitted scopes stay admin",
			request: map[string]any{"name": "operator", "password": "correct-horse"},
			want:    []string{controller.ScopeAdmin},
		},
		{
			name: "narrow scopes are honored",
			request: map[string]any{
				"name": "viewer", "password": "correct-horse",
				"scopes": []string{controller.ScopeRunsRead, controller.ScopeLogsRead},
			},
			want: []string{controller.ScopeRunsRead, controller.ScopeLogsRead},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ts := userScopesServer(t)
			bootstrapAdmin(t, ts)
			if status, body := postUserJSON(t, ts.URL+"/api/v1/users", test.request); status != http.StatusCreated {
				t.Fatalf("create user = %d: %s", status, body)
			}
			status, body := postUserJSON(t, ts.URL+"/api/v1/auth/login", map[string]string{
				"username": test.request["name"].(string),
				"password": "correct-horse",
			})
			if status != http.StatusOK {
				t.Fatalf("login = %d: %s", status, body)
			}
			var login struct {
				SessionID string   `json:"session_id"`
				Scopes    []string `json:"scopes"`
			}
			if err := json.Unmarshal(body, &login); err != nil {
				t.Fatalf("decode login %s: %v", body, err)
			}
			if !slices.Equal(login.Scopes, test.want) {
				t.Errorf("login scopes = %v, want %v", login.Scopes, test.want)
			}

			req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/auth/session", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Session "+login.SessionID)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("resolve session: %v", err)
			}
			defer resp.Body.Close()
			var session struct {
				Scopes []string `json:"scopes"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
				t.Fatalf("decode session: %v", err)
			}
			if !slices.Equal(session.Scopes, test.want) {
				t.Errorf("resolved session scopes = %v, want %v", session.Scopes, test.want)
			}
		})
	}
}

func TestCreateUser_RejectsUnknownScope(t *testing.T) {
	ts := userScopesServer(t)
	bootstrapAdmin(t, ts)
	status, body := postUserJSON(t, ts.URL+"/api/v1/users", map[string]any{
		"name": "typo", "password": "correct-horse", "scopes": []string{"runs.reed"},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("create user with unknown scope = %d: %s", status, body)
	}
	if !bytes.Contains(body, []byte("runs.reed")) {
		t.Errorf("error should name the offending scope; got %s", body)
	}
}

func TestCreateUser_RejectsMalformedScopeList(t *testing.T) {
	for _, test := range []struct {
		name   string
		scopes []string
		want   string
	}{
		{name: "empty entry", scopes: []string{""}, want: "empty scope"},
		{name: "blank entry beside a real one", scopes: []string{controller.ScopeRunsRead, "  "}, want: "empty scope"},
		{
			name:   "repeated entry",
			scopes: []string{controller.ScopeRunsRead, controller.ScopeRunsRead},
			want:   "duplicate scope",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ts := userScopesServer(t)
			bootstrapAdmin(t, ts)
			status, body := postUserJSON(t, ts.URL+"/api/v1/users", map[string]any{
				"name": "narrow", "password": "correct-horse", "scopes": test.scopes,
			})
			if status != http.StatusBadRequest {
				t.Fatalf("create user with %v = %d: %s", test.scopes, status, body)
			}
			if !bytes.Contains(body, []byte(test.want)) {
				t.Errorf("error should name the problem %q; got %s", test.want, body)
			}
		})
	}
}

func TestListUsers_ReportsScopes(t *testing.T) {
	ts := userScopesServer(t)
	bootstrapAdmin(t, ts)
	if status, body := postUserJSON(t, ts.URL+"/api/v1/users", map[string]any{
		"name": "viewer", "password": "correct-horse",
		"scopes": []string{controller.ScopeRunsRead},
	}); status != http.StatusCreated {
		t.Fatalf("create viewer = %d: %s", status, body)
	}
	resp, err := http.Get(ts.URL + "/api/v1/users")
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Users []struct {
			Name   string   `json:"name"`
			Scopes []string `json:"scopes"`
		} `json:"users"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode users: %v", err)
	}
	got := map[string][]string{}
	for _, u := range out.Users {
		got[u.Name] = u.Scopes
	}
	if !slices.Equal(got["viewer"], []string{controller.ScopeRunsRead}) {
		t.Errorf("viewer scopes = %v, want [%s]", got["viewer"], controller.ScopeRunsRead)
	}
	if !slices.Equal(got["root"], []string{controller.ScopeAdmin}) {
		t.Errorf("root scopes = %v, want [%s]", got["root"], controller.ScopeAdmin)
	}
}

func TestUsersSchema_ExistingRowsDefaultToAdmin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.DB().Exec(
		`INSERT INTO users (name, pw_hash, created_at) VALUES (?, ?, ?)`,
		"legacy", "argon2id$00$00", time.Now().Unix(),
	); err != nil {
		t.Fatalf("insert legacy user: %v", err)
	}
	users, err := st.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 || !slices.Equal(users[0].Scopes, []string{controller.ScopeAdmin}) {
		t.Fatalf("legacy user scopes = %+v, want [%s]", users, controller.ScopeAdmin)
	}
}
