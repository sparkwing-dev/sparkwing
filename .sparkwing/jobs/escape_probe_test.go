package jobs

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func stubClient(status int, err error) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: status, Body: http.NoBody}, nil
	})}
}

// A probe reports pass when what it reaches is fenced off and fail when it is
// not, and never prints the value it read.
func TestEscapeProbe_ReportsPassFailWithoutLeakingValues(t *testing.T) {
	reachable := prober{
		env:        []string{"AWS_SECRET_ACCESS_KEY=AKIAsecretvalue", "PATH=/usr/bin"},
		readFile:   func(string) ([]byte, error) { return []byte("a-real-token-value"), nil },
		dial:       func(_, _ string, _ time.Duration) (net.Conn, error) { return nil, nil },
		httpClient: stubClient(http.StatusOK, nil),
	}
	for _, r := range reachable.run() {
		if !r.succeeded {
			t.Errorf("%s: want fail when everything is reachable", r.name)
		}
		if strings.Contains(r.detail, "AKIA") || strings.Contains(r.detail, "a-real-token-value") ||
			strings.Contains(r.detail, "AWS_SECRET_ACCESS_KEY") {
			t.Errorf("%s: detail leaks a value: %q", r.name, r.detail)
		}
	}

	fenced := prober{
		env:        []string{"PATH=/usr/bin", "HOME=/tmp"},
		readFile:   func(string) ([]byte, error) { return nil, errors.New("no such file") },
		dial:       func(_, _ string, _ time.Duration) (net.Conn, error) { return nil, errors.New("connection refused") },
		httpClient: stubClient(0, errors.New("no route")),
	}
	for _, r := range fenced.run() {
		if r.name == "env credential" {
			continue
		}
		if r.succeeded {
			t.Errorf("%s: want pass when nothing is reachable", r.name)
		}
	}
}

func TestEscapeProbe_EnvProbeMatchesOnlyCredentialNames(t *testing.T) {
	if (prober{env: []string{"PATH=/usr/bin", "HOME=/tmp", "GOMAXPROCS=4"}}).envProbe().succeeded {
		t.Fatal("env probe fired on non-credential variables")
	}
	for _, kv := range []string{"GITHUB_TOKEN=x", "aws_access_key_id=x", "DB_PASSWORD=x"} {
		if !(prober{env: []string{kv}}).envProbe().succeeded {
			t.Errorf("env probe missed %q", kv)
		}
	}
}

// Every fenced service the design names, including the Stratum ports, has a
// probe, so a policy that stops blocking one turns the run red.
func TestEscapeProbe_CoversEveryFencedService(t *testing.T) {
	for _, want := range []string{"dind.sparkwing.svc.cluster.local:2375", ":5432", "argocd", "loki", "tempo", ":3333", ":4444", ":14444"} {
		found := false
		for _, addr := range tcpTargets {
			if strings.Contains(addr, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no probe targets %q", want)
		}
	}
}
