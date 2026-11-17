// Command slack-run-events receives Sparkwing run events, verifies their
// Standard Webhooks signature and posts each one to a Slack incoming webhook.
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	h := handler(os.Getenv("SPARKWING_WEBHOOK_SECRET"), os.Getenv("SLACK_WEBHOOK_URL"), &http.Client{Timeout: 5 * time.Second})
	log.Fatal(http.ListenAndServe(addr, h))
}

func handler(secret, slackURL string, client *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil || !verified(secret, r.Header, body, time.Now()) {
			http.Error(w, "signature does not verify", http.StatusUnauthorized)
			return
		}
		var event struct {
			Data struct {
				Pipeline  string `json:"pipeline"`
				RunNumber int64  `json:"run_number"`
				Status    string `json:"status"`
				URL       string `json:"url"`
				Git       struct {
					Branch string `json:"branch"`
				} `json:"git"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			http.Error(w, "not a run event", http.StatusBadRequest)
			return
		}
		d := event.Data
		text := fmt.Sprintf("%s #%d %s", d.Pipeline, d.RunNumber, d.Status)
		if d.Git.Branch != "" {
			text += " on " + d.Git.Branch
		}
		if d.URL != "" {
			text += " " + d.URL
		}
		message, err := json.Marshal(map[string]string{"text": text})
		if err != nil {
			http.Error(w, "cannot encode the message", http.StatusInternalServerError)
			return
		}
		// #nosec G704 -- slackURL is the operator's SLACK_WEBHOOK_URL; the signed event only supplies the message text.
		resp, err := client.Post(slackURL, "application/json", bytes.NewReader(message))
		if err != nil {
			http.Error(w, "slack unreachable", http.StatusBadGateway)
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			http.Error(w, "slack refused the message", http.StatusBadGateway)
		}
	}
}

func verified(secret string, h http.Header, body []byte, now time.Time) bool {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	stamp := h.Get("webhook-timestamp")
	unix, terr := strconv.ParseInt(stamp, 10, 64)
	if err != nil || len(key) == 0 || terr != nil || now.Sub(time.Unix(unix, 0)).Abs() > 5*time.Minute {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(h.Get("webhook-id") + "." + stamp + "."))
	mac.Write(body)
	want := mac.Sum(nil)
	for _, signature := range strings.Fields(h.Get("webhook-signature")) {
		got, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(signature, "v1,"))
		if strings.HasPrefix(signature, "v1,") && err == nil && hmac.Equal(got, want) {
			return true
		}
	}
	return false
}
