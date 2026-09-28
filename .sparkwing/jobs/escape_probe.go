package jobs

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// EscapeProbeArgs names the cluster-specific targets the probe cannot derive.
// A missing target fails the run, so the gate never passes by skipping one.
type EscapeProbeArgs struct {
	RDS      string `flag:"rds" desc:"The RDS endpoint as host:port; required."`
	APIHosts string `flag:"api-hosts" desc:"Comma-separated API server endpoint IPs behind the kubernetes Service VIP; required."`
	Nodes    string `flag:"nodes" desc:"Comma-separated node IPs whose kubelet (10250) a pod must not reach, beyond its own node."`
}

// EscapeProbe checks that a customer-code pod cannot reach the credentials and
// services the containment design fences off. It probes at plan time and in a
// work node, reports pass or fail per probe and never a value, and fails the
// run if any probe reaches what it should not.
type EscapeProbe struct {
	sparkwing.Base
	args EscapeProbeArgs
}

func (EscapeProbe) ShortHelp() string {
	return "Negative-control pipeline: proves customer pods cannot reach cluster credentials or services"
}

func (EscapeProbe) Help() string {
	return "Runs a fixed set of probes in the plan and in a node pod: service-account and IRSA token files, " +
		"credential-named environment variables other than the pod's own claim token, Kubernetes API " +
		"authorization at the Service VIP and each endpoint (SelfSubjectRulesReview, a dry-run Job mounting " +
		"sparkwing-database, GET secrets), a controller admin route with the pod's own token, IMDSv2, the " +
		"kubelet on its own and named nodes, and TCP to dind, RDS, argocd, loki, tempo and the Stratum ports. " +
		"It prints pass or fail per probe, never a value, and fails the run if any probe succeeds or a " +
		"required target is missing. Run it in a throwaway team with no credit or secrets."
}

func (EscapeProbe) Examples() []sparkwing.Example {
	return []sparkwing.Example{{
		Comment: "Probe from a plan and a node pod",
		Command: "sparkwing run escape-probe --rds db.example:5432 --api-hosts 10.0.36.1,10.0.65.130",
	}}
}

// safety: detail names what was tried, never what was found, so a red run's
// logs carry no leaked credential.
type probeResult struct {
	name      string
	succeeded bool
	detail    string
}

type prober struct {
	args       EscapeProbeArgs
	env        []string
	readFile   func(string) ([]byte, error)
	dial       func(network, address string, timeout time.Duration) (net.Conn, error)
	httpClient *http.Client
}

func defaultProber(args EscapeProbeArgs) prober {
	return prober{
		args:     args,
		env:      os.Environ(),
		readFile: os.ReadFile,
		dial:     net.DialTimeout,
		httpClient: &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
			// #nosec G402 -- the probe asks whether the API answers at all; trusting its certificate would add nothing
			TLSClientConfig: insecureTLS(),
		}},
	}
}

// safety: SPARKWING_AGENT_TOKEN is the pod's own claim token, the one credential
// it is meant to hold, so the credential-name probe passes over that name alone.
const ownTokenEnv = "SPARKWING_AGENT_TOKEN"

// safety: the probe reports only whether an env key contains one of these,
// never the key or its value.
var credentialNames = []string{"TOKEN", "SECRET", "PASSWORD", "AWS_", "GITHUB_", "NETRC", "KEY"}

const (
	saTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	kubeletPort = "10250"
	apiVIP      = "kubernetes.default.svc:443"
)

// safety: these are the fenced services and the Stratum mining ports a hijacked
// pod would dial; a probe that connects to any of them fails the run.
var tcpTargets = map[string]string{
	"dind":      "dind.sparkwing.svc.cluster.local:2375",
	"argocd":    "argocd-server.argocd.svc.cluster.local:443",
	"loki":      "loki.monitoring.svc.cluster.local:3100",
	"tempo":     "tempo.monitoring.svc.cluster.local:3200",
	"stratum-a": "pool.supportxmr.com:3333",
	"stratum-b": "pool.supportxmr.com:4444",
	"stratum-c": "pool.supportxmr.com:14444",
}

func (p prober) run() []probeResult {
	out := p.fileProbes()
	out = append(out, p.envProbe(), p.imdsProbe(), p.adminRouteProbe())
	for name, addr := range tcpTargets {
		out = append(out, p.tcpProbe("tcp "+name, addr))
	}
	out = append(out, p.required("tcp rds", p.args.RDS, func(addr string) probeResult { return p.tcpProbe("tcp rds", addr) })...)
	apis := []string{apiVIP}
	for _, host := range splitList(p.args.APIHosts) {
		apis = append(apis, net.JoinHostPort(host, "443"))
	}
	if len(apis) == 1 {
		out = append(out, missingTarget("kube-api endpoints", "--api-hosts"))
	}
	for _, api := range apis {
		out = append(out, p.kubeProbes(api)...)
	}
	nodes := splitList(p.args.Nodes)
	if gw := p.ownNode(); gw != "" {
		nodes = append(nodes, gw)
	}
	for _, node := range nodes {
		out = append(out, p.tcpProbe("kubelet "+node, net.JoinHostPort(node, kubeletPort)))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func missingTarget(name, flag string) probeResult {
	return probeResult{name: name, succeeded: true, detail: "not probed: " + flag + " is required"}
}

func (p prober) required(name, target string, probe func(string) probeResult) []probeResult {
	if target == "" {
		return []probeResult{missingTarget(name, "--"+strings.TrimPrefix(name, "tcp "))}
	}
	return []probeResult{probe(target)}
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (p prober) fileProbes() []probeResult {
	var out []probeResult
	for _, path := range []string{saTokenFile, p.getenv("AWS_WEB_IDENTITY_TOKEN_FILE"), filepath.Join(p.getenv("HOME"), ".netrc")} {
		if path == "" || path == ".netrc" {
			continue
		}
		_, err := p.readFile(path)
		out = append(out, probeResult{name: "read " + filepath.Base(path), succeeded: err == nil, detail: "reads a credential file"})
	}
	return out
}

func (p prober) getenv(key string) string {
	for _, kv := range p.env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

func (p prober) envProbe() probeResult {
	for _, kv := range p.env {
		key, _, _ := strings.Cut(kv, "=")
		if key == ownTokenEnv {
			continue
		}
		up := strings.ToUpper(key)
		for _, name := range credentialNames {
			if strings.Contains(up, name) {
				return probeResult{name: "env credential", succeeded: true, detail: "a credential-named variable other than the claim token is set"}
			}
		}
	}
	return probeResult{name: "env credential", detail: "no credential-named variable other than the claim token is set"}
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

// safety: the status code is the whole verdict, so the body is drained unread
// and never reaches a result.
func (p prober) status(method, url, bearer string, body []byte, header map[string]string) int {
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return 0
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return 0
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return 0
	}
	if err := resp.Body.Close(); err != nil {
		return 0
	}
	return resp.StatusCode
}

func (p prober) imdsProbe() probeResult {
	code := p.status(http.MethodPut, "http://169.254.169.254/latest/api/token", "", nil,
		map[string]string{"X-aws-ec2-metadata-token-ttl-seconds": "60"})
	return probeResult{name: "imds", succeeded: code == http.StatusOK, detail: "fetches an IMDSv2 token"}
}

// safety: the pod's own claim token must open no route beyond its claim, so an
// operator route that answers 2xx to it is a breach.
func (p prober) adminRouteProbe() probeResult {
	base := p.getenv("SPARKWING_CONTROLLER_URL")
	if base == "" {
		return missingTarget("controller admin route", "SPARKWING_CONTROLLER_URL")
	}
	code := p.status(http.MethodGet, strings.TrimRight(base, "/")+"/api/v1/tokens", p.getenv(ownTokenEnv), nil, nil)
	return probeResult{
		name: "controller admin route", succeeded: code >= 200 && code < 300,
		detail: "lists tokens with the pod's own claim token",
	}
}

const dryRunJob = `{"apiVersion":"batch/v1","kind":"Job","metadata":{"generateName":"escape-probe-"},
"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"c","image":"busybox",
"volumeMounts":[{"name":"s","mountPath":"/s"}]}],"volumes":[{"name":"s","secret":{"secretName":"sparkwing-database"}}]}}}}`

// safety: each call carries the mounted service-account token when one leaks
// into the pod, and none otherwise, so both an exposed token and anonymous
// access are judged by what the API authorizes, not by whether it answers.
func (p prober) kubeProbes(api string) []probeResult {
	bearer := ""
	if tok, err := p.readFile(saTokenFile); err == nil {
		bearer = strings.TrimSpace(string(tok))
	}
	base := "https://" + api
	jsonHeader := map[string]string{"Content-Type": "application/json"}
	rules := p.rulesGrantSensitive(base, bearer)
	job := p.status(http.MethodPost, base+"/apis/batch/v1/namespaces/sparkwing/jobs?dryRun=All", bearer, []byte(dryRunJob), jsonHeader)
	secrets := p.status(http.MethodGet, base+"/api/v1/namespaces/sparkwing/secrets", bearer, nil, nil)
	return []probeResult{
		{name: "kube-api " + api + " rules", succeeded: rules, detail: "SelfSubjectRulesReview grants jobs or secrets"},
		{name: "kube-api " + api + " dry-run job", succeeded: job >= 200 && job < 300, detail: "dry-run creates a Job mounting sparkwing-database"},
		{name: "kube-api " + api + " secrets", succeeded: secrets == http.StatusOK, detail: "lists secrets in sparkwing"},
	}
}

func (p prober) rulesGrantSensitive(base, bearer string) bool {
	body := []byte(`{"apiVersion":"authorization.k8s.io/v1","kind":"SelfSubjectRulesReview","spec":{"namespace":"sparkwing"}}`)
	req, err := http.NewRequest(http.MethodPost, base+"/apis/authorization.k8s.io/v1/selfsubjectrulesreviews", bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return false
	}
	var review struct {
		Status struct {
			ResourceRules []struct {
				Resources []string `json:"resources"`
			} `json:"resourceRules"`
		} `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&review); err != nil {
		return false
	}
	for _, rule := range review.Status.ResourceRules {
		for _, r := range rule.Resources {
			if r == "*" || r == "jobs" || r == "secrets" {
				return true
			}
		}
	}
	return false
}

// safety: NetworkPolicy never blocks a pod's traffic to its own node, and the
// pod's default gateway is that node, so the kubelet there is always probed.
func (p prober) ownNode() string {
	route, err := p.readFile("/proc/net/route")
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(route))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || f[1] != "00000000" {
			continue
		}
		raw, err := hex.DecodeString(f[2])
		if err != nil || len(raw) != 4 {
			continue
		}
		return net.IPv4(raw[3], raw[2], raw[1], raw[0]).String()
	}
	return ""
}

func reportProbes(ctx context.Context, where string, results []probeResult) error {
	var breached []string
	for _, r := range results {
		verdict := "pass"
		if r.succeeded {
			verdict, breached = "FAIL", append(breached, r.name)
		}
		sparkwing.Info(ctx, "%s: %s: %s (%s)", where, verdict, r.name, r.detail)
	}
	if len(breached) > 0 {
		return fmt.Errorf("%s: containment breached or unprobed: %s", where, strings.Join(breached, ", "))
	}
	return nil
}

type escapeProbeNode struct {
	sparkwing.Base
	args EscapeProbeArgs
}

func (j *escapeProbeNode) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "probe", func(ctx context.Context) error {
		return reportProbes(ctx, "node", defaultProber(j.args).run())
	}), nil
}

func (p *EscapeProbe) Plan(ctx context.Context, plan *sparkwing.Plan, in EscapeProbeArgs, _ sparkwing.RunContext) error {
	if err := reportProbes(ctx, "plan", defaultProber(in).run()); err != nil {
		return err
	}
	sparkwing.Job(plan, "probe-node", &escapeProbeNode{args: in})
	return nil
}

func insecureTLS() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- see defaultProber
}

func init() {
	sparkwing.Register[EscapeProbeArgs]("escape-probe", func() sparkwing.Pipeline[EscapeProbeArgs] { return &EscapeProbe{} })
}
