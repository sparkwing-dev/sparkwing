package webhook

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

const testSecret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"

func TestSignMatchesStandardWebhooksVector(t *testing.T) {
	got, err := Sign(testSecret, "msg_p5jXN8AQM9LWM0D4loKWxJek", time.Unix(1614265330, 0), []byte(`{"test": 2432232314}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="; got != want {
		t.Fatalf("signature = %s, want %s", got, want)
	}
	for _, bad := range []string{"MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw", "whsec_", "whsec_!!"} {
		if _, err := Sign(bad, "id", time.Unix(1, 0), nil); err == nil {
			t.Fatalf("secret %q signed", bad)
		}
	}
}

func verify(t *testing.T, r *http.Request, body []byte) {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(testSecret[len("whsec_"):])
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(r.Header.Get("webhook-id") + "." + r.Header.Get("webhook-timestamp") + "." + string(body)))
	want := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(r.Header.Get("webhook-signature")), []byte(want)) {
		t.Errorf("signature %q does not verify", r.Header.Get("webhook-signature"))
	}
}

func TestSendSignsWithFixedHeadersOnly(t *testing.T) {
	body := []byte(`{"specversion":"1.0","id":"run_1/2"}`)
	at := time.Unix(1_800_000_000, 0)
	var seen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		got, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || string(got) != string(body) {
			t.Errorf("method %s body %s", r.Method, got)
		}
		verify(t, r, got)
		want := map[string]string{"Content-Type": "application/cloudevents+json", "User-Agent": "Sparkwing-Webhooks/v1.2.3", "Webhook-Id": "run_1/2", "Webhook-Timestamp": strconv.FormatInt(at.Unix(), 10)}
		for k, v := range want {
			if r.Header.Get(k) != v {
				t.Errorf("%s = %q, want %q", k, r.Header.Get(k), v)
			}
		}
		allowed := []string{"Content-Type", "User-Agent", "Webhook-Id", "Webhook-Timestamp", "Webhook-Signature", "Content-Length", "Accept-Encoding", "Connection"}
		for k := range r.Header {
			if !slices.Contains(allowed, k) {
				t.Errorf("unexpected header %s", k)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	r := NewSender(Policy{Local: true}).Send(t.Context(), Attempt{URL: srv.URL + "/hook", EventID: "run_1/2", Body: body, Secret: testSecret, UserAgent: UserAgent("v1.2.3"), At: at})
	if !r.Delivered || r.Status != http.StatusNoContent || r.Code != "" || seen.Load() != 1 {
		t.Fatalf("result %+v after %d requests", r, seen.Load())
	}
}

func TestSendClassifiesNonSuccess(t *testing.T) {
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	var redirected atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/target", func(http.ResponseWriter, *http.Request) { redirected.Add(1) })
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/target", http.StatusFound) })
	mux.HandleFunc("/fail", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	mux.HandleFunc("/slow-down", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", r.URL.Query().Get("after"))
		w.WriteHeader(http.StatusTooManyRequests)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	sender := NewSender(Policy{Local: true})
	send := func(path string) Result {
		return sender.Send(t.Context(), Attempt{URL: srv.URL + path, EventID: "e", Body: []byte("{}"), Secret: testSecret, At: at})
	}
	minutes := func(m int) *time.Duration { d := time.Duration(m) * time.Minute; return &d }
	for _, tc := range []struct {
		path   string
		status int
		after  *time.Duration
	}{
		{"/fail", 500, nil},
		{"/redirect", 302, nil},
		{"/slow-down?after=120", 429, minutes(2)},
		{"/slow-down?after=7200", 429, minutes(60)},
		{"/slow-down?after=" + url.QueryEscape(at.Add(5*time.Minute).Format(http.TimeFormat)), 429, minutes(5)},
		{"/slow-down?after=soon", 429, nil},
	} {
		r := send(tc.path)
		if r.Delivered || r.Status != tc.status || r.Code != CodeHTTPStatus {
			t.Errorf("%s: %+v", tc.path, r)
		}
		if (r.RetryAfter == nil) != (tc.after == nil) || r.RetryAfter != nil && *r.RetryAfter != *tc.after {
			t.Errorf("%s: retry after %v, want %v", tc.path, r.RetryAfter, tc.after)
		}
	}
	if redirected.Load() != 0 {
		t.Fatal("redirect was followed")
	}
	if r := sender.Send(t.Context(), Attempt{URL: srv.URL, EventID: "e", Secret: "plain", At: at}); r.Code != CodeSecret {
		t.Fatalf("bad secret: %+v", r)
	}
}

func TestControllerPolicyPinsPublicAddresses(t *testing.T) {
	answers := map[string][]string{
		"public.example.test":   {"93.184.216.34"},
		"rebind.example.test":   {"93.184.216.34", "10.0.0.7"},
		"loopback.example.test": {"127.0.0.1"},
		"mapped.example.test":   {"::ffff:127.0.0.1"},
		"nat64.example.test":    {"64:ff9b::a00:1"},
		"metadata.example.test": {"169.254.169.254"},
	}
	lookup := func(_ context.Context, host string) ([]net.IPAddr, error) {
		var out []net.IPAddr
		for _, a := range answers[host] {
			out = append(out, net.IPAddr{IP: net.ParseIP(a)})
		}
		return out, nil
	}
	var dialed []string
	serverName := make(chan string, 1)
	dial := func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			_ = tls.Server(server, &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
				serverName <- h.ServerName
				return nil, io.EOF
			}}).Handshake()
		}()
		return client, nil
	}
	sender := NewSender(Policy{Lookup: lookup, Dial: dial})
	send := func(u string) Result {
		return sender.Send(t.Context(), Attempt{URL: u, EventID: "e", Secret: testSecret, At: time.Unix(1, 0)})
	}
	for _, u := range []string{
		"http://public.example.test/hook",
		"https://rebind.example.test/hook",
		"https://loopback.example.test/hook",
		"https://mapped.example.test/hook",
		"https://nat64.example.test/hook",
		"https://metadata.example.test/hook",
		"https://127.0.0.1/hook",
		"https://10.0.0.1/hook",
		"https://169.254.169.254/latest",
		"https://[::ffff:127.0.0.1]/hook",
		"https://localhost/hook",
		"https://user:pw@public.example.test/hook",
		"https://public.example.test/hook#frag",
	} {
		if r := send(u); r.Code != CodePolicy {
			t.Errorf("%s: %+v", u, r)
		}
	}
	if len(dialed) != 0 {
		t.Fatalf("refused destinations dialed %v", dialed)
	}
	r := send("https://public.example.test:8443/hook")
	if !slices.Equal(dialed, []string{"93.184.216.34:8443"}) || r.Code != CodeTransport && r.Code != CodeTLS {
		t.Fatalf("dialed %v, result %+v", dialed, r)
	}
	if got := <-serverName; got != "public.example.test" {
		t.Fatalf("TLS server name %q", got)
	}
}

func TestSendTimesOutAfterAttemptTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dial := func(context.Context, string, string) (net.Conn, error) {
			client, server := net.Pipe()
			go func() {
				defer server.Close()
				if _, err := http.ReadRequest(bufio.NewReader(server)); err != nil {
					return
				}
				_, _ = io.Copy(io.Discard, server)
			}()
			return client, nil
		}
		start := time.Now()
		r := NewSender(Policy{Local: true, Dial: dial}).Send(t.Context(), Attempt{URL: "http://receiver.test/hook", EventID: "e", Secret: testSecret, At: start})
		if r.Code != CodeTimeout || r.Delivered {
			t.Fatalf("result %+v", r)
		}
		if waited := time.Since(start); waited != AttemptTimeout {
			t.Fatalf("waited %v, want %v", waited, AttemptTimeout)
		}
	})
}

func TestDelayUsesHalfToFullJitterAndRetryAfterOnlyLengthensIt(t *testing.T) {
	low := func(int64) int64 { return 0 }
	high := func(n int64) int64 { return n - 1 }
	for attempt, base := range map[int64]time.Duration{1: 10 * time.Second, 2: 30 * time.Second, 3: 2 * time.Minute, 4: 10 * time.Minute, 5: 30 * time.Minute, 6: time.Hour, 40: time.Hour} {
		if got := Delay(attempt, Result{}, low); got != base/2 {
			t.Errorf("attempt %d low = %v", attempt, got)
		}
		if got := Delay(attempt, Result{}, high); got != base {
			t.Errorf("attempt %d high = %v", attempt, got)
		}
	}
	for _, tc := range []struct{ after, want time.Duration }{
		{0, 5 * time.Second},
		{time.Second, 5 * time.Second},
		{2 * time.Minute, 2 * time.Minute},
	} {
		if got := Delay(1, Result{RetryAfter: &tc.after}, low); got != tc.want {
			t.Errorf("Retry-After %v after attempt 1 waits %v, want %v", tc.after, got, tc.want)
		}
	}
}
