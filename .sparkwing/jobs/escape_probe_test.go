package jobs

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const secretValue = "swc_the-claim-token-value"

func cluster(open bool, bearers *[]string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		*bearers = append(*bearers, r.Header.Get("Authorization"))
		if !open {
			return nil, errors.New("connection refused")
		}
		status, body := http.StatusOK, ""
		switch {
		case strings.HasSuffix(r.URL.Path, "selfsubjectrulesreviews"):
			status, body = http.StatusCreated, `{"status":{"resourceRules":[{"resources":["secrets"]}]}}`
		case strings.HasSuffix(r.URL.Path, "/jobs"):
			status = http.StatusCreated
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}

func testProber(open bool, bearers *[]string) prober {
	env := []string{"PATH=/usr/bin", "HOME=/tmp", "SPARKWING_AGENT_TOKEN=" + secretValue, "SPARKWING_CONTROLLER_URL=http://controller"}
	p := prober{
		args: EscapeProbeArgs{RDS: "db:5432", APIHosts: "10.0.36.1"},
		env:  env,
		readFile: func(path string) ([]byte, error) {
			if path == "/proc/net/route" {
				return []byte("Iface\tDestination\tGateway\neth0\t00000000\t0101FEA9\n"), nil
			}
			return nil, errors.New("no such file")
		},
		dial:       func(_, _ string, _ time.Duration) (net.Conn, error) { return nil, errors.New("refused") },
		httpClient: cluster(open, bearers),
	}
	if open {
		p.env = append(p.env, "AWS_SECRET_ACCESS_KEY=AKIAvalue")
		fenced := p.readFile
		p.readFile = func(path string) ([]byte, error) {
			if path == "/proc/net/route" {
				return fenced(path)
			}
			return []byte("sa-token-value"), nil
		}
		p.dial = func(_, _ string, _ time.Duration) (net.Conn, error) { return nil, nil }
	}
	return p
}

func byName(results []probeResult) map[string]probeResult {
	out := map[string]probeResult{}
	for _, r := range results {
		out[r.name] = r
	}
	return out
}

// A fenced pod passes every probe, including with its own claim token set: the
// token it is meant to hold is not a leak.
func TestEscapeProbe_AFencedPodPassesEveryProbe(t *testing.T) {
	var bearers []string
	for _, r := range testProber(false, &bearers).run() {
		if r.succeeded {
			t.Errorf("%s: failed a fenced pod (%s)", r.name, r.detail)
		}
	}
}

// An open pod fails the credential, API authorization, admin route, IMDS,
// kubelet and TCP probes, and no result carries a value it read.
func TestEscapeProbe_AnOpenPodFailsAndLeaksNoValue(t *testing.T) {
	var bearers []string
	results := byName(testProber(true, &bearers).run())
	for _, name := range []string{
		"env credential", "read token", "imds", "controller admin route", "tcp rds", "tcp dind", "tcp stratum-a",
		"kube-api kubernetes.default.svc:443 rules", "kube-api 10.0.36.1:443 dry-run job",
		"kube-api 10.0.36.1:443 secrets", "kubelet 169.254.1.1",
	} {
		if r, ok := results[name]; !ok || !r.succeeded {
			t.Errorf("%s: %+v, want a failed probe", name, r)
		}
	}
	for _, r := range results {
		for _, value := range []string{secretValue, "AKIAvalue", "sa-token-value", "AWS_SECRET_ACCESS_KEY"} {
			if strings.Contains(r.name+r.detail, value) {
				t.Errorf("%s leaks %q", r.name, value)
			}
		}
	}
	var sawOwnToken, sawSAToken bool
	for _, b := range bearers {
		sawOwnToken = sawOwnToken || b == "Bearer "+secretValue
		sawSAToken = sawSAToken || b == "Bearer sa-token-value"
	}
	if !sawOwnToken || !sawSAToken {
		t.Fatalf("the admin route or API probes did not carry the pod's own credentials: %v", bearers)
	}
}

func TestEscapeProbe_AMissingTargetFailsTheGate(t *testing.T) {
	var bearers []string
	p := testProber(false, &bearers)
	p.args = EscapeProbeArgs{}
	results := byName(p.run())
	for _, name := range []string{"tcp rds", "kube-api endpoints"} {
		if r := results[name]; !r.succeeded || !strings.Contains(r.detail, "required") {
			t.Errorf("%s: %+v, want an unprobed target to fail", name, r)
		}
	}
}

func TestEscapeProbe_EnvProbeSkipsOnlyTheClaimToken(t *testing.T) {
	if (prober{env: []string{"SPARKWING_AGENT_TOKEN=x", "PATH=/usr/bin"}}).envProbe().succeeded {
		t.Fatal("the pod's own claim token failed the env probe")
	}
	for _, kv := range []string{"GITHUB_TOKEN=x", "aws_access_key_id=x", "DB_PASSWORD=x", "SPARKWING_AGENT_TOKEN2=x"} {
		if !(prober{env: []string{kv}}).envProbe().succeeded {
			t.Errorf("env probe missed %q", kv)
		}
	}
}
