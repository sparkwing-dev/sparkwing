package githubapp_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
)

func TestRequestMemoRepeatsOnlySettledReads(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[string]int{}
	throttled := true
	client := githubapp.New(githubapp.Config{AppID: 1, PrivateKey: key, HTTP: &http.Client{Transport: refTransport(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		seen[r.Method+" "+r.URL.Path]++
		code, body := http.StatusOK, ""
		switch r.Method + " " + r.URL.Path {
		case "POST /app/installations/7/access_tokens":
			body = fmt.Sprintf(`{"token":"token-%d","expires_at":%q}`, seen[r.Method+" "+r.URL.Path], time.Now().Add(time.Hour).Format(time.RFC3339))
		case "GET /repos/acme/widgets/commits/" + commit:
			body = `{"sha":"` + commit + `"}`
		case "GET /repos/acme/widgets/contents/.sparkwing/sparkwing.yaml":
			if throttled {
				throttled = false
				code, body = http.StatusTooManyRequests, `{}`
			} else {
				code, body = http.StatusNotFound, `{}`
			}
		case "GET /repos/acme/widgets/contents/big.yaml":
			body = strings.Repeat("a", 2<<20)
		case "POST /repos/acme/widgets/check-runs":
			body = `{"id":5}`
		default:
			code, body = http.StatusNotFound, `{}`
		}
		mu.Unlock()
		return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}})

	resolve := func(ctx context.Context) {
		t.Helper()
		if got, err := client.ResolveCommit(ctx, 7, "acme", "widgets", commit); err != nil || got != commit {
			t.Fatalf("ResolveCommit = %q, %v", got, err)
		}
	}
	memo := githubapp.WithRequestMemo(t.Context())
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { resolve(memo) })
	}
	wg.Wait()
	if seen["POST /app/installations/7/access_tokens"] != 1 || seen["GET /repos/acme/widgets/commits/"+commit] != 1 {
		t.Fatalf("four concurrent identical resolutions under one memo reached GitHub %v", seen)
	}

	for range 2 {
		if _, err := client.CreateCheckRun(memo, "token", "acme", "widgets", githubapp.CheckRun{Name: "ci", HeadSHA: commit}); err != nil {
			t.Fatal(err)
		}
	}
	if seen["POST /repos/acme/widgets/check-runs"] != 2 {
		t.Fatalf("a write was answered from memory: %v", seen)
	}

	for range 3 {
		_, _, _ = client.FileAtCommit(memo, 7, 42, "acme", "widgets", commit, ".sparkwing/sparkwing.yaml")
	}
	if seen["GET /repos/acme/widgets/contents/.sparkwing/sparkwing.yaml"] != 2 {
		t.Fatalf("a throttled read must be asked again and a settled 404 kept: %v", seen)
	}

	for range 2 {
		_, _, _ = client.FileAtCommit(memo, 7, 42, "acme", "widgets", commit, "big.yaml")
	}
	if seen["GET /repos/acme/widgets/contents/big.yaml"] != 2 {
		t.Fatalf("an answer past the memo cap was kept: %v", seen)
	}

	resolve(t.Context())
	if seen["GET /repos/acme/widgets/commits/"+commit] != 2 {
		t.Fatalf("a request without the memo reused another request's answer: %v", seen)
	}
}
