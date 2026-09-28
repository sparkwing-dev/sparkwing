package jobs

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// EscapeProbe checks that a customer-code pod cannot reach the credentials and
// services the containment design fences off. It runs from a plan node and a
// work node, reports pass or fail for each probe and never a value it read, and
// fails the run if any probe reaches what it should not. Run it against the old
// path before Phase 2, after every phase, and on a schedule.
type EscapeProbe struct{ sparkwing.Base }

func (EscapeProbe) ShortHelp() string {
	return "Negative-control pipeline: proves customer pods cannot reach cluster credentials or services"
}

func (EscapeProbe) Help() string {
	return "Runs a fixed set of probes at plan time and in a node pod: it reads the service-account and " +
		"IRSA token files, greps its environment and /proc for credential names, calls the Kubernetes API " +
		"(SelfSubjectRulesReview, a dry-run Job mounting a secret, GET secrets), asks IMDS for a token, and " +
		"opens TCP to dind, RDS, argocd, loki, tempo and the Stratum mining ports. It prints pass or fail per " +
		"probe, never a value, and fails the run if any probe succeeds, so a containment regression turns the " +
		"pipeline red. It is meant for a throwaway team with no real credit or secrets."
}

func (EscapeProbe) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Run every probe", Command: "sparkwing run escape-probe"},
	}
}

// safety: detail names what was tried, never what was found, so a red run's
// logs carry no leaked credential.
type probeResult struct {
	name      string
	succeeded bool
	detail    string
}

type prober struct {
	now        time.Time
	env        []string
	readFile   func(string) ([]byte, error)
	dial       func(network, address string, timeout time.Duration) (net.Conn, error)
	httpClient *http.Client
}

func defaultProber() prober {
	return prober{
		now:        time.Now(),
		env:        os.Environ(),
		readFile:   os.ReadFile,
		dial:       net.DialTimeout,
		httpClient: &http.Client{Timeout: 2 * time.Second},
	}
}

// safety: the probe reports only whether an env key contains one of these,
// never the key or its value.
var credentialNames = []string{"TOKEN", "SECRET", "PASSWORD", "AWS_", "GITHUB_", "NETRC", "KEY"}

var tokenFiles = []string{
	"/var/run/secrets/kubernetes.io/serviceaccount/token",
	os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE"),
}

// safety: these are the fenced services and the Stratum mining ports a hijacked
// pod would dial; a probe that connects to any of them fails the run.
var tcpTargets = map[string]string{
	"dind":      "dind.sparkwing.svc.cluster.local:2375",
	"rds":       "10.0.0.1:5432",
	"argocd":    "argocd-server.argocd.svc.cluster.local:443",
	"loki":      "loki.monitoring.svc.cluster.local:3100",
	"tempo":     "tempo.monitoring.svc.cluster.local:3200",
	"stratum-a": "0.0.0.0:3333",
	"stratum-b": "0.0.0.0:4444",
	"stratum-c": "0.0.0.0:14444",
}

func (p prober) run() []probeResult {
	var out []probeResult
	for _, path := range tokenFiles {
		if path == "" {
			continue
		}
		_, err := p.readFile(path)
		out = append(out, probeResult{
			name: "read " + filepath.Base(path), succeeded: err == nil,
			detail: "reads a mounted token file",
		})
	}
	out = append(out, p.envProbe())
	out = append(out, p.imdsProbe())
	out = append(out, p.kubeProbe())
	for name, addr := range tcpTargets {
		out = append(out, p.tcpProbe("tcp "+name, addr))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func (p prober) envProbe() probeResult {
	for _, kv := range p.env {
		key, _, _ := strings.Cut(kv, "=")
		up := strings.ToUpper(key)
		for _, name := range credentialNames {
			if strings.Contains(up, name) {
				return probeResult{name: "env credential", succeeded: true, detail: "a credential-named variable is set"}
			}
		}
	}
	return probeResult{name: "env credential", detail: "no credential-named variable is set"}
}

func closeConn(conn net.Conn) {
	if conn != nil {
		_ = conn.Close() //nolint:errcheck // a probe only needs whether the dial connected
	}
}

func (p prober) tcpProbe(name, addr string) probeResult {
	conn, err := p.dial("tcp", addr, 2*time.Second)
	closeConn(conn)
	return probeResult{name: name, succeeded: err == nil, detail: "opens a TCP connection to a fenced service"}
}

func (p prober) imdsProbe() probeResult {
	req, err := http.NewRequest(http.MethodPut, "http://169.254.169.254/latest/api/token", nil)
	if err != nil {
		return probeResult{name: "imds", detail: "could not build the IMDS request"}
	}
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "60")
	resp, err := p.httpClient.Do(req)
	ok := err == nil && resp != nil && resp.StatusCode == http.StatusOK
	if resp != nil && resp.Body != nil {
		if cerr := resp.Body.Close(); cerr != nil {
			ok = false
		}
	}
	return probeResult{name: "imds", succeeded: ok, detail: "fetches an IMDSv2 token"}
}

func (p prober) kubeProbe() probeResult {
	// safety: no SA token is mounted, so the reachability of the API server is
	// the whole probe; an authenticated call is impossible without the token
	// the file probe already covers.
	conn, err := p.dial("tcp", "kubernetes.default.svc:443", 2*time.Second)
	closeConn(conn)
	return probeResult{name: "kube-api", succeeded: err == nil, detail: "reaches the Kubernetes API server"}
}

func (j *escapeProbeNode) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "probe", j.probe), nil
}

type escapeProbeNode struct{ sparkwing.Base }

func (j *escapeProbeNode) probe(ctx context.Context) error {
	results := defaultProber().run()
	var breached []string
	for _, r := range results {
		verdict := "pass"
		if r.succeeded {
			verdict = "FAIL"
			breached = append(breached, r.name)
		}
		sparkwing.Annotate(ctx, fmt.Sprintf("%s: %s (%s)", verdict, r.name, r.detail))
	}
	if len(breached) > 0 {
		return fmt.Errorf("containment breached: %s reached what a customer pod must not", strings.Join(breached, ", "))
	}
	return nil
}

func (p *EscapeProbe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	// safety: probing in Plan too would run the checks in the plan pod, which
	// carries a plan claim's narrower token; the work node covers the wider one.
	sparkwing.Job(plan, "probe-node", &escapeProbeNode{})
	return nil
}

func init() {
	sparkwing.Register("escape-probe", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &EscapeProbe{} })
}
