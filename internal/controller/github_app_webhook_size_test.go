package controller_test

import (
	"bytes"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
)

func TestGitHubAppWebhookBoundsBodiesWithoutBufferingUnsignedOnes(t *testing.T) {
	f := newAppFixture(t)
	post := func(body []byte, signature string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, f.url+"/webhooks/github-app", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-GitHub-Event", "ping")
		req.Header.Set("X-GitHub-Delivery", "delivery-"+signature[len(signature)-12:])
		req.Header.Set("X-Hub-Signature-256", signature)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	padded := func(size int) []byte {
		head, tail := `{"zen":"`, `"}`
		return []byte(head + strings.Repeat("z", size-len(head)-len(tail)) + tail)
	}
	large := padded(20 << 20)
	if code := post(large, githubapptest.Sign(large)); code != http.StatusAccepted {
		t.Fatalf("signed 20 MiB delivery = %d", code)
	}
	over := padded(25<<20 + 1)
	if code := post(over, githubapptest.Sign(over)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("delivery above 25 MiB = %d", code)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if code := post(large, githubapptest.SignWith("forged", large)); code != http.StatusUnauthorized {
		t.Fatalf("unsigned delivery = %d", code)
	}
	runtime.ReadMemStats(&after)
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 8<<20 {
		t.Fatalf("an unsigned 20 MiB delivery allocated %d bytes", grown)
	}
}
