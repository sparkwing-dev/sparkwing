package controller_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	reads  int
	writes int
}

func (d *deadlineRecorder) SetReadDeadline(time.Time) error {
	d.reads++
	return nil
}

func (d *deadlineRecorder) SetWriteDeadline(time.Time) error {
	d.writes++
	return nil
}

func TestGitcacheStreamDeadline_OnlyExtendsForAnAuthenticatedCaller(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	raw, _, err := st.CreateToken("admin", store.TokenKindUser,
		[]string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	handler := controller.New(st, nil).EnableAuthFromStore().Handler()

	for _, tc := range []struct {
		name        string
		token       string
		wantExtends bool
	}{
		{name: "anonymous"},
		{name: "admin bearer", token: raw, wantExtends: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, rerr := http.NewRequestWithContext(context.Background(), http.MethodGet,
				"/api/v1/gitcache/git/widgets/info/refs?service=git-upload-pack", nil)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			handler.ServeHTTP(rec, req)

			rejected := rec.Code == http.StatusUnauthorized
			if rejected == tc.wantExtends {
				t.Fatalf("status = %d for token %q", rec.Code, tc.token)
			}
			extended := rec.reads > 0 || rec.writes > 0
			if extended != tc.wantExtends {
				t.Fatalf("deadline extended = %v (reads %d, writes %d), want %v",
					extended, rec.reads, rec.writes, tc.wantExtends)
			}
		})
	}
}

func TestGitcacheStreamDeadline_CoversTheRegisterRoute(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	raw, _, err := st.CreateToken("admin", store.TokenKindUser,
		[]string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	handler := controller.New(st, nil).EnableAuthFromStore().Handler()

	for _, tc := range []struct {
		name        string
		token       string
		wantExtends bool
	}{
		{name: "anonymous"},
		{name: "admin bearer", token: raw, wantExtends: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, rerr := http.NewRequestWithContext(context.Background(), http.MethodPost,
				"/api/v1/gitcache/git/register?name=widgets&repo=https://git.example.com/acme/widgets.git", nil)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			handler.ServeHTTP(rec, req)

			extended := rec.reads > 0 || rec.writes > 0
			if extended != tc.wantExtends {
				t.Fatalf("deadline extended = %v (reads %d, writes %d), want %v",
					extended, rec.reads, rec.writes, tc.wantExtends)
			}
		})
	}
}
