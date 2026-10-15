package controller_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestEnableAuthFromStoreRefusesAnonymousBeforeBootstrapProperty(t *testing.T) {
	for _, licensing := range []string{"single_team", "license_before", "license_after"} {
		t.Run(licensing, func(t *testing.T) {
			st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			var logs bytes.Buffer
			s := controller.New(st, slog.New(slog.NewTextHandler(&logs, nil)))
			if licensing != "single_team" {
				raw, key := multiTeamLicense(t)
				lic := license.Resolve(raw, key, time.Now(), nil)
				if licensing == "license_before" {
					s.WithLicense(lic).EnableAuthFromStore()
				} else {
					s.EnableAuthFromStore().WithLicense(lic)
				}
			} else {
				s.EnableAuthFromStore()
			}
			if !s.AuthEnabled() {
				t.Fatal("startup disabled authentication before bootstrap")
			}
			h := s.Handler()
			property := func(selector []byte) bool {
				forged := hex.EncodeToString(selector)
				for _, path := range []string{"/api/v1/runs", "/api/v1/tokens", "/api/v1/runners"} {
					r := httptest.NewRequest("GET", path+"?team="+forged, nil)
					r.Header.Set("X-Sparkwing-Team", forged)
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					if w.Code != http.StatusUnauthorized || len(w.Result().Cookies()) != 0 {
						t.Logf("%s: HTTP %d %s", path, w.Code, w.Body)
						return false
					}
				}
				return true
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/health", nil))
			var health struct {
				Auth string `json:"auth"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &health) != nil || health.Auth != "enabled" {
				t.Fatalf("health: HTTP %d %s", w.Code, w.Body)
			}
			if strings.Contains(logs.String(), "serving unauthenticated") {
				t.Fatal("startup advertised anonymous access")
			}
		})
	}
}
