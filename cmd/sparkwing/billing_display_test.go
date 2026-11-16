package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func runBillingDisplay(t *testing.T, args []string, response any) string {
	t.Helper()
	connect := func(_ context.Context, profile string) (controllerwire.BillingManager, func(), error) {
		if profile != "work" {
			t.Fatalf("profile = %q", profile)
		}
		manager, err := client.NewBillingManager(client.EngineOptions{URL: "https://controller.test", HTTP: &http.Client{Transport: logRoundTrip(func(r *http.Request) (*http.Response, error) {
			w := httptest.NewRecorder()
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(response); err != nil {
				t.Fatal(err)
			}
			return w.Result(), nil
		})}})
		return manager, func() {}, err
	}
	var out bytes.Buffer
	if err := runBillingArgs(t.Context(), append(args, "--profile", "work"), &out, connect); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func assertBillingHumanText(t *testing.T, got, want string, lines int) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("output = %q, want framed quoted value %q", got, want)
	}
	if strings.ContainsFunc(got, func(r rune) bool { return r != '\n' && unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) }) {
		t.Fatalf("active controls in %q", got)
	}
	if strings.Count(got, "\n") != lines {
		t.Fatalf("untrusted newline changed layout: %q", got)
	}
}

func TestBillingCLIQuotesDecisionAndFailure(t *testing.T) {
	for _, tc := range []struct{ name, control string }{{"escape", "\x1b[2J"}, {"newline", "\n"}, {"bidi", "\u202e"}} {
		t.Run(tc.name, func(t *testing.T) {
			input := "operator" + tc.control + "message"
			standing := controllerwire.TeamStanding{Tier: "New", Request: &controllerwire.StandingRequest{Tier: "High", State: "denied", RequestedAt: 1, DecisionReason: input}}
			refill := controllerwire.TeamRefill{FailedAt: 1, Failure: input}
			response := map[string]any{"standing": standing, "refill": refill}
			t.Run("standing", func(t *testing.T) {
				got := runBillingDisplay(t, []string{"standing", "-o", "pretty"}, response)
				assertBillingHumanText(t, got, "Request: High denied, sent 1970-01-01T00:00:01Z ("+strconv.Quote(input)+")\n", 4)
			})
			t.Run("refill", func(t *testing.T) {
				got := runBillingDisplay(t, []string{"refill", "-o", "pretty"}, response)
				assertBillingHumanText(t, got, "The last refill failed at 1970-01-01T00:00:01Z ("+strconv.Quote(input)+"). Update your card", 7)
			})
			t.Run("JSON", func(t *testing.T) {
				var got controllerwire.TeamStanding
				if err := json.Unmarshal([]byte(runBillingDisplay(t, []string{"standing", "-o", "json"}, response)), &got); err != nil {
					t.Fatal(err)
				}
				if got.Request == nil || got.Request.DecisionReason != input {
					t.Fatalf("JSON decision changed: %+v", got.Request)
				}
				var result controllerwire.TeamRefill
				if err := json.Unmarshal([]byte(runBillingDisplay(t, []string{"refill", "-o", "json"}, response)), &result); err != nil {
					t.Fatal(err)
				}
				if result.Failure != input {
					t.Fatalf("JSON failure changed: %q", result.Failure)
				}
			})
		})
	}
}

func TestBillingCLIQuotesServerMetadata(t *testing.T) {
	input := "future\x1b[2J\n\u202etier"
	quoted := strconv.Quote(input)
	response := map[string]any{"standing": controllerwire.TeamStanding{Tier: input, History: controllerwire.StandingHistory{Tier: input}, PrepayCeiling: input, Request: &controllerwire.StandingRequest{Tier: input, State: input, RequestedAt: 1}}, "refill": controllerwire.TeamRefill{Enabled: true, Limit: input}}
	t.Run("standing", func(t *testing.T) {
		got := runBillingDisplay(t, []string{"standing", "-o", "pretty"}, response)
		assertBillingHumanText(t, got, "Standing: "+quoted+"\nHistory tier: "+quoted+"\nPrepaid funds can reach: "+quoted+"\nRequest: "+quoted+" "+quoted+", sent ", 4)
	})
	t.Run("refill", func(t *testing.T) {
		got := runBillingDisplay(t, []string{"refill", "-o", "pretty"}, response)
		assertBillingHumanText(t, got, "The next refill is stopped by "+quoted+"; Cloud work stops", 7)
	})
	t.Run("request-tier", func(t *testing.T) {
		got := runBillingDisplay(t, []string{"request-tier", "--tier", "high", "--message", "more capacity", "-o", "pretty"}, controllerwire.StandingRequest{ID: input, Tier: input, State: "open"})
		assertBillingHumanText(t, got, "Sent your request for "+quoted+" standing, higher limits or larger payments ("+quoted+"). An operator", 1)
	})
}
