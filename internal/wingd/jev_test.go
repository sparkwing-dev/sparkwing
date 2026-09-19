package wingd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTypeSafeJevAdvisorUsesTypedChoice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("authorization = %q", got)
		}
		var request struct {
			Model     string                    `json:"model"`
			Questions map[string]map[string]any `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "jev-latest" || request.Questions["admission"]["type"] != "choice" {
			t.Fatalf("request = %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"admission":{"type":"choice","choice":"admit_short_backfill","probabilities":{"admit_short_backfill":0.91,"wait":0.09},"confidence":0.82}},"usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
	defer server.Close()
	advisor := &typesafeJevAdvisor{apiKey: "secret", endpoint: server.URL, model: "jev-latest", client: server.Client()}
	answer, err := advisor.Advise(context.Background(), JevState{Candidate: JevCandidate{Class: "interactive", ExpectedP99MS: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	if !answer.Admit || answer.Confidence != 0.82 || answer.Probability != 0.91 || answer.Model != "jev-1.13.0" {
		t.Fatalf("answer = %+v", answer)
	}
}

func TestTypeSafeJevAdvisorRejectsUnknownChoice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"jev-test","answers":{"admission":{"type":"choice","choice":"cancel_everything","confidence":1}}}`))
	}))
	defer server.Close()
	advisor := &typesafeJevAdvisor{apiKey: "secret", endpoint: server.URL, model: "jev-latest", client: server.Client()}
	if _, err := advisor.Advise(context.Background(), JevState{}); err == nil {
		t.Fatal("unknown choice was accepted")
	}
}
