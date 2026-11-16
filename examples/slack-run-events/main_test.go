package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runevents"
	"github.com/sparkwing-dev/sparkwing/internal/runevents/webhook"
)

type fakeSlack struct {
	status int
	texts  []string
}

func (f *fakeSlack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var message struct{ Text string }
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &message)
	f.texts = append(f.texts, message.Text)
	w.WriteHeader(f.status)
}

func newSecret(t *testing.T) string {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return "whsec_" + base64.StdEncoding.EncodeToString(key)
}

func finishedEvent(t *testing.T, at time.Time) []byte {
	body, err := runevents.Build(runevents.EventFinished, runevents.Facts{
		Source: "https://sparkwing.example/teams/acme", StateSeq: 3, Time: at, CreatedAt: at,
		RunID: "run-1", RunNumber: 42, Pipeline: "deploy", Status: "failed",
		GitBranch: "main", GitSHA: "abc123", URL: "https://sparkwing.example/runs/run-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func deliver(t *testing.T, h http.Handler, signWith string, signedAt time.Time, body []byte) int {
	return deliverSigned(t, h, signWith, signedAt, body, body)
}

func deliverSigned(t *testing.T, h http.Handler, signWith string, signedAt time.Time, signed, sent []byte) int {
	signature, err := webhook.Sign(signWith, "run-1/3", signedAt, signed)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(sent)))
	req.Header.Set("Content-Type", runevents.ContentType)
	req.Header.Set("Webhook-Id", "run-1/3")
	req.Header.Set("Webhook-Timestamp", strconv.FormatInt(signedAt.Unix(), 10))
	req.Header.Set("Webhook-Signature", signature)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestReceiverPostsAVerifiedRunEventToSlack(t *testing.T) {
	slack := &fakeSlack{status: http.StatusOK}
	server := httptest.NewServer(slack)
	defer server.Close()
	secret, now := newSecret(t), time.Now()

	if code := deliver(t, handler(secret, server.URL, server.Client()), secret, now, finishedEvent(t, now)); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	want := "deploy #42 failed on main https://sparkwing.example/runs/run-1"
	if len(slack.texts) != 1 || slack.texts[0] != want {
		t.Fatalf("slack received %q, want [%q]", slack.texts, want)
	}
}

func TestReceiverRefusesUnverifiedEventsWithoutPosting(t *testing.T) {
	slack := &fakeSlack{status: http.StatusOK}
	server := httptest.NewServer(slack)
	defer server.Close()
	secret, now := newSecret(t), time.Now()
	h := handler(secret, server.URL, server.Client())
	body := finishedEvent(t, now)

	cases := map[string]int{
		"another secret":  deliver(t, h, newSecret(t), now, body),
		"stale timestamp": deliver(t, h, secret, now.Add(-10*time.Minute), body),
		"altered body":    deliverSigned(t, h, secret, now, body, []byte(strings.Replace(string(body), "failed", "success", 1))),
	}
	for name, code := range cases {
		if code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, code)
		}
	}
	if len(slack.texts) != 0 {
		t.Fatalf("slack received %q from unverified events", slack.texts)
	}
}

func TestReceiverAsksForARetryWhenSlackFails(t *testing.T) {
	server := httptest.NewServer(&fakeSlack{status: http.StatusInternalServerError})
	defer server.Close()
	secret, now := newSecret(t), time.Now()

	if code := deliver(t, handler(secret, server.URL, server.Client()), secret, now, finishedEvent(t, now)); code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 so Sparkwing retries", code)
	}
}
