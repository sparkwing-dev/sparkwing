package jobs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serveDocsIndex(t *testing.T, status int, body string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server.URL + "/llms.txt"
}

func TestPublishedDocsRefuseACutWhileTheLastReleaseIsUndocumented(t *testing.T) {
	url := serveDocsIndex(t, http.StatusOK, "# sparkwing -- v0.77.0 (latest)\n\n> docs\n")
	err := checkPublishedDocsName(t.Context(), url, "v0.80.0")
	if err == nil || !strings.Contains(err.Error(), "documents v0.77.0 but v0.80.0 is released") {
		t.Fatalf("stale public docs refusal = %v", err)
	}
}

func TestPublishedDocsAcceptTheReleasedVersion(t *testing.T) {
	url := serveDocsIndex(t, http.StatusOK, "# sparkwing -- v0.80.0 (latest)\n")
	if err := checkPublishedDocsName(t.Context(), url, "v0.80.0"); err != nil {
		t.Fatalf("current public docs refused: %v", err)
	}
}

func TestPublishedDocsRefuseAnIndexTheyCannotRead(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"denied":   {http.StatusForbidden, "AccessDenied"},
		"untitled": {http.StatusOK, "# something else\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := checkPublishedDocsName(t.Context(), serveDocsIndex(t, tc.status, tc.body), "v0.80.0"); err == nil {
				t.Fatal("an unreadable docs index passed as current")
			}
		})
	}
}
