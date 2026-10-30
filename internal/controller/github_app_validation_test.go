package controller_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestGitHubAppReleaseSubscriptionValidationExplainsTagFilter(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	for _, event := range []string{"release_published", "release_prereleased"} {
		t.Run(event, func(t *testing.T) {
			body := map[string]any{"repository": "acme/widgets", "pipeline": "release", event: true}
			var out struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if code := f.call("PUT", "/api/v1/team/github-app/triggers", olga.auth, body, &out); code != http.StatusBadRequest {
				t.Fatalf("release subscription without tag filter = %d, want 400", code)
			}
			if out.Error.Code != "bad_request" || !strings.Contains(out.Error.Message, "a release subscription needs release_tags patterns") {
				t.Fatalf("release subscription validation = %+v, want release_tags remedy", out.Error)
			}
			body["release_tags"] = []string{"v*"}
			if code := f.call("PUT", "/api/v1/team/github-app/triggers", olga.auth, body, nil); code != http.StatusOK {
				t.Fatalf("release subscription with tag filter = %d, want 200", code)
			}
		})
	}
}
