package jobs

import (
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const secretValue = "swc_the-claim-token-value"

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var (
	refused     = &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	dnsMiss     = &net.DNSError{Err: "no such host", Name: "dind", IsNotFound: true}
	errTLSBroke = errors.New("tls: handshake failure")
)

func cluster(status int, rules string, err error, bearers *[]string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		*bearers = append(*bearers, r.Header.Get("Authorization"))
		if r.URL.Host == "github.com" {
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		}
		if err != nil {
			return nil, err
		}
		code, body := status, ""
		if strings.HasSuffix(r.URL.Path, "selfsubjectrulesreviews") && status == http.StatusCreated {
			body = rules
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}

func routeFile(path string) ([]byte, error) {
	if path == "/proc/net/route" {
		return []byte("Iface\tDestination\tGateway\neth0\t00000000\t0101FEA9\n"), nil
	}
	return nil, fs.ErrNotExist
}

func fencedProber(bearers *[]string) prober {
	return prober{
		args:       EscapeProbeArgs{RDS: "db:5432", APIHosts: "10.0.36.1"},
		env:        []string{"PATH=/usr/bin", "HOME=/tmp", "SPARKWING_AGENT_TOKEN=" + secretValue, "SPARKWING_CONTROLLER_URL=http://controller", "SPARKWING_RUN_ID=run-probe"},
		readFile:   routeFile,
		dial:       openInternet(func(string) error { return refused }),
		httpClient: cluster(http.StatusForbidden, "", nil, bearers),
	}
}

func openInternet(fenced func(addr string) error) func(string, string, time.Duration) (net.Conn, error) {
	return func(_, addr string, _ time.Duration) (net.Conn, error) {
		if addr == publicNonWeb {
			return nil, nil
		}
		return nil, fenced(addr)
	}
}

func byName(results []probeResult) map[string]probeResult {
	out := map[string]probeResult{}
	for _, r := range results {
		out[r.name] = r
	}
	return out
}

// A fenced pod passes: every endpoint either denies it or the network drops
// or refuses it, and its own claim token is not a leak.
func TestEscapeProbe_AFencedPodPassesEveryProbe(t *testing.T) {
	var bearers []string
	for name, p := range map[string]prober{
		"denied": fencedProber(&bearers),
		"dropped": func() prober {
			p := fencedProber(&bearers)
			p.httpClient = cluster(0, "", timeoutErr{}, &bearers)
			return p
		}(),
	} {
		for _, r := range p.run() {
			if r.verdict.failsGate() {
				t.Errorf("%s: %s: %s (%s)", name, r.name, r.verdict, r.detail)
			}
		}
	}
}

// An open pod fails the credential, API authorization, admin route, IMDS,
// kubelet and TCP probes, and no result carries a value it read.
func TestEscapeProbe_AnOpenPodFailsAndLeaksNoValue(t *testing.T) {
	var bearers []string
	p := fencedProber(&bearers)
	p.env = append(p.env, "AWS_SECRET_ACCESS_KEY=AKIAvalue")
	p.readFile = func(path string) ([]byte, error) {
		if path == "/proc/net/route" {
			return routeFile(path)
		}
		return []byte("sa-token-value"), nil
	}
	p.dial = func(_, _ string, _ time.Duration) (net.Conn, error) { return nil, nil }
	p.httpClient = cluster(http.StatusCreated, `{"status":{"resourceRules":[{"resources":["secrets"]}]}}`, nil, &bearers)
	results := byName(p.run())
	for _, name := range []string{
		"env credential", "read token", "imds", "controller admin route", "controller git credential", "controller source credential", "tcp rds", "tcp dind", "tcp loki",
		"kube-api kubernetes.default.svc:443 rules", "kube-api 10.0.36.1:443 dry-run job",
		"kube-api 10.0.36.1:443 secrets", "kubelet 169.254.1.1",
	} {
		if r, ok := results[name]; !ok || r.verdict != reached {
			t.Errorf("%s: %+v, want reached", name, r)
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

// A probe that errors for any reason but a drop or a refusal, or that gets an
// answer that is neither a grant nor a denial, has proved nothing, so it fails
// the gate rather than passing it.
func TestEscapeProbe_AnUnsettledProbeFailsTheGate(t *testing.T) {
	var bearers []string
	dns := fencedProber(&bearers)
	dns.dial = func(_, _ string, _ time.Duration) (net.Conn, error) { return nil, dnsMiss }
	tls := fencedProber(&bearers)
	tls.httpClient = cluster(0, "", errTLSBroke, &bearers)
	notFound := fencedProber(&bearers)
	notFound.httpClient = cluster(http.StatusNotFound, "", nil, &bearers)
	unreadable := fencedProber(&bearers)
	unreadable.readFile = func(string) ([]byte, error) { return nil, errors.New("I/O error") }
	for name, c := range map[string]struct {
		p     prober
		probe string
	}{
		"dns miss":         {dns, "tcp dind"},
		"tls failure":      {tls, "kube-api 10.0.36.1:443 secrets"},
		"tls on rules":     {tls, "kube-api kubernetes.default.svc:443 rules"},
		"404 from the api": {notFound, "kube-api 10.0.36.1:443 dry-run job"},
		"404 on imds":      {notFound, "imds"},
		"no route file":    {unreadable, "kubelet own node"},
		"unreadable token": {unreadable, "read token"},
	} {
		r, ok := byName(c.p.run())[c.probe]
		if !ok || r.verdict != inconclusive || !r.verdict.failsGate() {
			t.Errorf("%s: %s = %+v, want inconclusive", name, c.probe, r)
		}
	}
}

func TestEscapeProbe_AMissingTargetFailsTheGate(t *testing.T) {
	var bearers []string
	p := fencedProber(&bearers)
	p.args = EscapeProbeArgs{}
	results := byName(p.run())
	for _, name := range []string{"tcp rds", "kube-api endpoints"} {
		if r := results[name]; !r.verdict.failsGate() || !strings.Contains(r.detail, "required") {
			t.Errorf("%s: %+v, want an unprobed target to fail", name, r)
		}
	}
}

func TestEscapeProbe_EnvProbeSkipsOnlyTheClaimToken(t *testing.T) {
	if (prober{env: []string{"SPARKWING_AGENT_TOKEN=x", "PATH=/usr/bin"}}).envProbe().verdict.failsGate() {
		t.Fatal("the pod's own claim token failed the env probe")
	}
	for _, kv := range []string{"GITHUB_TOKEN=x", "aws_access_key_id=x", "DB_PASSWORD=x", "SPARKWING_AGENT_TOKEN2=x"} {
		if !(prober{env: []string{kv}}).envProbe().verdict.failsGate() {
			t.Errorf("env probe missed %q", kv)
		}
	}
}

func TestEscapeProbe_NetVerdictSeparatesFenceFromFailure(t *testing.T) {
	dial := func(errno syscall.Errno) error { return &net.OpError{Op: "dial", Net: "tcp", Err: errno} }
	for name, c := range map[string]struct {
		err  error
		want verdict
	}{
		"connected":        {nil, reached},
		"refused":          {dial(syscall.ECONNREFUSED), blocked},
		"host unreachable": {dial(syscall.EHOSTUNREACH), blocked},
		"net unreachable":  {dial(syscall.ENETUNREACH), blocked},
		"dropped":          {timeoutErr{}, blocked},
		"dns miss":         {&net.DNSError{Err: "no such host", IsNotFound: true}, inconclusive},
		"dns timeout":      {&net.DNSError{Err: "i/o timeout", IsTimeout: true}, inconclusive},
		"tls failure":      {errTLSBroke, inconclusive},
	} {
		if got := netVerdict(c.err); got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("connection reset") }
func (failingBody) Close() error             { return errors.New("connection reset") }

// Once a status arrived, the probe is judged by it, whatever happens to the
// body after it.
func TestEscapeProbe_AStatusSettlesTheProbeWhateverTheBodyDoes(t *testing.T) {
	for status, want := range map[int]verdict{http.StatusOK: reached, http.StatusForbidden: denied, http.StatusNotFound: inconclusive} {
		p := prober{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: failingBody{}}, nil
		})}}
		code, err := p.status(http.MethodGet, "http://api/x", "", nil, nil)
		if got := httpVerdict(code, err); got != want {
			t.Errorf("status %d with a failing body: %s, want %s", status, got, want)
		}
	}
}

func TestEscapeProbe_RulesReviewVerdicts(t *testing.T) {
	var bearers []string
	for name, c := range map[string]struct {
		rules string
		want  verdict
	}{
		"grants secrets":       {`{"status":{"resourceRules":[{"resources":["secrets"]}]}}`, reached},
		"grants nothing":       {`{"status":{"resourceRules":[{"resources":["selfsubjectreviews"]}]}}`, denied},
		"incomplete, no grant": {`{"status":{"resourceRules":[],"incomplete":true}}`, inconclusive},
		"incomplete, a grant":  {`{"status":{"resourceRules":[{"resources":["*"]}],"incomplete":true}}`, reached},
		"unreadable":           {`{"status":`, inconclusive},
	} {
		p := prober{httpClient: cluster(http.StatusCreated, c.rules, nil, &bearers)}
		if got := p.rulesVerdict("https://api", ""); got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
}

// The earlier web-and-git-only policy passed every negative probe, so only the
// positive ones can catch it: port 53 on the public resolver is dropped, and
// the gate must fail on it. With no internet at all, HTTPS fails too.
func TestEscapeProbe_ATooTightPolicyFailsTheGate(t *testing.T) {
	var bearers []string
	webOnly := fencedProber(&bearers)
	webOnly.dial = func(_, addr string, _ time.Duration) (net.Conn, error) {
		if addr == publicNonWeb {
			return nil, timeoutErr{}
		}
		return nil, refused
	}
	if r := byName(webOnly.run())["egress tcp public non-web port"]; r.verdict != closed || !r.verdict.failsGate() {
		t.Fatalf("a web-and-git-only policy: %+v, want the public non-web port closed", r)
	}
	offline := webOnly
	offline.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, timeoutErr{} })}
	if r := byName(offline.run())["egress https github.com"]; r.verdict != closed {
		t.Fatalf("no internet at all: %+v, want HTTPS closed", r)
	}
}

// A webhook run carries no arguments, so the repository's sparkwing.yaml must
// name every target the probe requires.
func TestEscapeProbe_TheRepositoryConfigNamesEveryRequiredTarget(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "sparkwing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	cfg, err := pipelines.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	entry := cfg.Find("escape-probe")
	if entry == nil {
		t.Fatal("sparkwing.yaml declares no escape-probe entry")
	}
	var bearers []string
	p := fencedProber(&bearers)
	p.args = EscapeProbeArgs{RDS: entry.Args["rds"], APIHosts: entry.Args["api-hosts"]}
	for _, r := range p.run() {
		if strings.Contains(r.detail, "is required") {
			t.Errorf("%s: %s", r.name, r.detail)
		}
	}
}

// A claim is refused another node's output and another run's; a pod that
// reads either, or cannot settle the question, fails the gate.
func TestEscapeProbe_OutputProbesFenceNonAncestorsAndOtherRuns(t *testing.T) {
	var bearers []string
	p := fencedProber(&bearers)
	p.args.OtherRun = "run-earlier"
	for _, r := range p.outputProbes() {
		if r.verdict != denied {
			t.Errorf("fenced: %s: %s, want denied", r.name, r.verdict)
		}
	}
	for _, b := range bearers {
		if b != "Bearer "+secretValue {
			t.Errorf("an output probe sent %q, want the pod's own claim token", b)
		}
	}
	open := p
	open.httpClient = cluster(http.StatusOK, "", nil, &bearers)
	for _, r := range open.outputProbes() {
		if r.verdict != reached || !r.verdict.failsGate() {
			t.Errorf("open: %s: %s, want REACHED", r.name, r.verdict)
		}
	}
	pending := p
	pending.httpClient = cluster(http.StatusConflict, "", nil, &bearers)
	if r := byName(pending.outputProbes())["controller non-ancestor output"]; !r.verdict.failsGate() {
		t.Errorf("a sibling that never finished: %s, want the gate to fail", r.verdict)
	}
	missing := fencedProber(&bearers)
	if r := byName(missing.outputProbes())["controller other run output"]; r.verdict != inconclusive {
		t.Errorf("no --other-run: %s, want INCONCLUSIVE", r.verdict)
	}
}
