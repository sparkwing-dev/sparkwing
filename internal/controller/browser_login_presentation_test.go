package controller_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestLoginRefusalsExplainUnknownReasonsAndOfferHelp(t *testing.T) {
	f := newIdentityFixture(t)
	for _, reason := range []string{"unknown_grid_reason", "<script>provider details</script>", "failed", "rate_limited"} {
		t.Run(reason, func(t *testing.T) {
			page := f.browserGet("/login?refused=" + url.QueryEscape(reason))
			if page.StatusCode != http.StatusOK {
				t.Fatalf("sign-in page status = %d", page.StatusCode)
			}
			for _, want := range []string{`role="alert"`, `href="mailto:support@sparkwing.dev"`, "Get help"} {
				if !strings.Contains(page.body, want) {
					t.Errorf("sign-in refusal lacks %q", want)
				}
			}
			if reason != "rate_limited" && !strings.Contains(page.body, "Sign-in failed on this server. Try again") {
				t.Error("sign-in refusal lacks a friendly explanation")
			}
			if strings.Contains(page.body, "provider details") || strings.Contains(page.body, "unknown_grid_reason") {
				t.Error("sign-in refusal reflects an unrecognized reason")
			}
		})
	}
	if page := f.browserGet("/login").body; strings.Contains(page, `role="alert"`) {
		t.Error("ordinary sign-in page shows a refusal")
	}
}
