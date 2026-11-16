package isolationprobe

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// safety: dial outcomes name what the network did, never what answered.
const (
	outcomeConnected       = "connected"
	outcomeTimeout         = "timeout"
	outcomeRefused         = "refused"
	outcomeHostUnreachable = "host-unreachable"
	outcomeNetUnreachable  = "net-unreachable"
	outcomeLocalDenied     = "local-denied"
	outcomeNoAddress       = "no-address"
	outcomeDNSFailure      = "dns-failure"
	outcomeError           = "error"
)

type network struct {
	dial        func(ctx context.Context, network, address string) (net.Conn, error)
	lookup      func(ctx context.Context, host string) ([]string, error)
	client      *http.Client
	dialTimeout time.Duration
	parallel    int
	now         func() time.Time
}

func newNetwork() network {
	d := &net.Dialer{}
	return network{
		dial:   d.DialContext,
		lookup: net.DefaultResolver.LookupHost,
		client: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DialContext: d.DialContext,
				Proxy:       nil,
				// #nosec G402 -- a probe asks whether an endpoint answers; trusting its certificate adds nothing
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		dialTimeout: 3 * time.Second,
		parallel:    16,
		now:         time.Now,
	}
}

// safety: a drop shows as a timeout and a reject as an unreachable host or network; a DNS failure
// says nothing about the fence, so it is classified before the timeout it may also be.
func classifyDial(err error) string {
	var dnsErr *net.DNSError
	var ne net.Error
	switch {
	case err == nil:
		return outcomeConnected
	case errors.As(err, &dnsErr):
		return outcomeDNSFailure
	case errors.Is(err, syscall.ECONNREFUSED):
		return outcomeRefused
	case errors.Is(err, syscall.EHOSTUNREACH):
		return outcomeHostUnreachable
	case errors.Is(err, syscall.ENETUNREACH):
		return outcomeNetUnreachable
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EACCES):
		return outcomeLocalDenied
	case errors.Is(err, syscall.EADDRNOTAVAIL), errors.Is(err, syscall.EAFNOSUPPORT):
		return outcomeNoAddress
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return outcomeTimeout
	default:
		return outcomeError
	}
}

// safety: a refused connection to a destination known to listen means the packet reached the
// host, so only the fence's own failure modes pass a blocked target.
func dialVerdict(t Target, outcome string) Verdict {
	switch t.Expect {
	case ExpectObserved:
		return Observed
	case ExpectOpen:
		if outcome == outcomeConnected {
			return Pass
		}
		return Inconclusive
	}
	switch outcome {
	case outcomeConnected:
		if t.Expect == ExpectDenied {
			return Inconclusive
		}
		return Breach
	case outcomeRefused:
		if t.Listening {
			return Breach
		}
		return Pass
	case outcomeTimeout, outcomeHostUnreachable, outcomeNetUnreachable, outcomeLocalDenied, outcomeNoAddress:
		return Pass
	default:
		return Inconclusive
	}
}

// safety: a denied target that accepted a connection passes only when every anonymous answer is a
// refusal; any success is a breach and anything else proves nothing.
func gradeVerdict(codes []int, err error) Verdict {
	if err != nil || len(codes) == 0 {
		return Inconclusive
	}
	refused := true
	for _, c := range codes {
		if c >= 200 && c < 300 {
			return Breach
		}
		if c != http.StatusUnauthorized && c != http.StatusForbidden {
			refused = false
		}
	}
	if refused {
		return Pass
	}
	return Inconclusive
}

func describeCodes(codes []int, err error) string {
	parts := make([]string, 0, len(codes)+1)
	for _, c := range codes {
		parts = append(parts, "http-"+strconv.Itoa(c))
	}
	if err != nil {
		parts = append(parts, "no-answer:"+classifyDial(err))
	}
	return strings.Join(parts, ",")
}

func (n network) probeAll(ctx context.Context, where string, targets []Target) []Result {
	out := make([]Result, len(targets))
	sem := make(chan struct{}, max(n.parallel, 1))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = n.probe(ctx, where, t)
		}()
	}
	wg.Wait()
	return out
}

func (n network) probe(ctx context.Context, where string, t Target) Result {
	start := n.now()
	dctx, cancel := context.WithTimeout(ctx, n.dialTimeout)
	conn, err := n.dial(dctx, "tcp", t.Address)
	cancel()
	if conn != nil {
		_ = conn.Close() //nolint:errcheck // a probe only needs whether the dial connected
	}
	outcome := classifyDial(err)
	r := Result{Item: t.Item, Context: where, Probe: t.Name, Target: t.Address, Expect: t.Expect, Outcome: outcome, Verdict: dialVerdict(t, outcome)}
	if outcome == outcomeConnected && t.Grade != "" {
		codes, gerr := n.grade(ctx, t)
		r.Outcome += "; " + describeCodes(codes, gerr)
		if t.Expect == ExpectDenied {
			r.Verdict = gradeVerdict(codes, gerr)
		}
	}
	r.Millis = n.now().Sub(start).Milliseconds()
	return r
}

// safety: the dry-run Job mounts a secret, so an API that admits it would let an anonymous caller
// read the namespace's secrets through a pod; dryRun=All persists nothing either way.
func kubeAPIRequests(base, namespace string) []gradeRequest {
	ns := url.PathEscape(namespace)
	rules, _ := json.Marshal(map[string]any{ //nolint:errcheck // a map of strings always marshals
		"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectRulesReview", "spec": map[string]string{"namespace": namespace},
	})
	jsonHeader := map[string]string{"Content-Type": "application/json"}
	return []gradeRequest{
		{http.MethodGet, base + "/api/v1/namespaces/" + ns + "/secrets", nil, nil},
		{http.MethodPost, base + "/apis/authorization.k8s.io/v1/selfsubjectrulesreviews", rules, jsonHeader},
		{http.MethodPost, base + "/apis/batch/v1/namespaces/" + ns + "/jobs?dryRun=All", []byte(dryRunJob), jsonHeader},
	}
}

const dryRunJob = `{"apiVersion":"batch/v1","kind":"Job","metadata":{"generateName":"isolation-probe-"},` +
	`"spec":{"template":{"spec":{"restartPolicy":"Never","automountServiceAccountToken":false,` +
	`"containers":[{"name":"probe","image":"isolation-probe.invalid/none","command":["true"],"volumeMounts":[{"name":"secret","mountPath":"/secret"}]}],` +
	`"volumes":[{"name":"secret","secret":{"secretName":"isolation-probe-absent"}}]}}}}`

type gradeRequest struct {
	method, url string
	body        []byte
	header      map[string]string
}

// safety: grading requests carry no credential and either only read or are dry runs, so an open
// endpoint answers with what an anonymous caller may do and nothing changes on it.
func (n network) grade(ctx context.Context, t Target) ([]int, error) {
	var reqs []gradeRequest
	switch t.Grade {
	case GradeKubelet:
		reqs = []gradeRequest{{http.MethodGet, "https://" + t.Address + "/pods", nil, nil}}
	case GradeKubeAPI:
		if !validName(t.Namespace) {
			return nil, errors.New("a kube API grade needs the namespace it asks about")
		}
		reqs = kubeAPIRequests("https://"+t.Address, t.Namespace)
	case GradeIMDS:
		reqs = []gradeRequest{
			{http.MethodPut, "http://" + t.Address + "/latest/api/token", nil, map[string]string{"X-aws-ec2-metadata-token-ttl-seconds": "60"}},
			{http.MethodGet, "http://" + t.Address + "/latest/meta-data/", nil, nil},
		}
	default:
		return nil, fmt.Errorf("unknown grade %q", t.Grade)
	}
	var codes []int
	var errs []error
	for _, r := range reqs {
		code, err := n.status(ctx, r.method, r.url, r.body, r.header)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		codes = append(codes, code)
	}
	return codes, errors.Join(errs...)
}

// safety: the status code is the whole answer, so the body is drained unread and never reaches a
// result.
func (n network) status(ctx context.Context, method, url string, body []byte, header map[string]string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) //nolint:errcheck // the status already answered the probe
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func (n network) positive(ctx context.Context, where string) []Result {
	out := make([]Result, 0, len(positiveChecks))
	for _, c := range positiveChecks {
		if c.kind == checkTCP {
			out = append(out, n.probe(ctx, where, Target{Item: "C6", Name: c.name, Address: c.target, Expect: ExpectOpen}))
			continue
		}
		start := n.now()
		r := Result{Item: "C6", Context: where, Probe: c.name, Target: c.target, Expect: ExpectOpen, Verdict: Pass}
		var err error
		if c.kind == checkDNS {
			lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, err = n.lookup(lctx, c.target)
			cancel()
			r.Outcome = "resolved"
		} else {
			var code int
			code, err = n.status(ctx, http.MethodGet, c.target, nil, nil)
			r.Outcome = "http-" + strconv.Itoa(code)
		}
		if err != nil {
			r.Outcome, r.Verdict = classifyDial(err), Inconclusive
		}
		r.Millis = n.now().Sub(start).Milliseconds()
		out = append(out, r)
	}
	return out
}

// safety: peers must fail to reach this listener while a connection from inside the pod succeeds,
// which proves the listener a peer's refusal is measured against.
func listen(port int) (net.Listener, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close() //nolint:errcheck // the listener answers connections and says nothing
		}
	}()
	return ln, nil
}

func podIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var ips []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 || dockerInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				ips = append(ips, ipn.IP.String())
			}
		}
	}
	sort.Strings(ips)
	return ips
}

func dockerInterface(name string) bool {
	return name == "docker0" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "br-")
}

func (n network) selfConnect(ctx context.Context, where string, ips []string, port int) []Result {
	if len(ips) == 0 {
		return []Result{{Item: "C9", Context: where, Probe: probeSelfConnect, Expect: ExpectOpen, Outcome: "no pod address", Verdict: Inconclusive}}
	}
	var out []Result
	for _, ip := range ips {
		out = append(out, n.probe(ctx, where, Target{Item: "C9", Name: probeSelfConnect, Address: net.JoinHostPort(ip, strconv.Itoa(port)), Expect: ExpectOpen}))
	}
	return out
}

// safety: the own node's kubelet must refuse an anonymous caller even where the fence lets the pod
// reach its own node, and every other node port must stay out of reach; a node never discovered
// is reported as a gap rather than passed.
func ownNodeTargets(node string) []Target {
	if node == "" {
		return nil
	}
	at := func(port string) string { return net.JoinHostPort(node, port) }
	blocked := func(name, port string) Target {
		return Target{Item: "C8", Name: "own node " + name, Address: at(port), Expect: ExpectBlocked}
	}
	return []Target{
		{Item: "C8", Name: "own node kubelet", Address: at("10250"), Expect: ExpectDenied, Listening: true, Grade: GradeKubelet},
		blocked("kubelet read-only port", "10255"),
		blocked("kube-proxy metrics", "10249"),
		blocked("kube-proxy health", "10256"),
		blocked("ssh", "22"),
		blocked("VPC CNI metrics", "61678"),
		blocked("VPC CNI introspection", "61679"),
		blocked("network policy agent metrics", "61680"),
	}
}

// safety: dockerd listens only on the scratch socket, so a Docker TCP port on the pod's own
// addresses would hand the daemon to anything that reaches the pod.
func ownPodDockerTargets(ips []string) []Target {
	var out []Target
	for _, ip := range append([]string{"127.0.0.1"}, ips...) {
		for _, port := range []string{"2375", "2376"} {
			out = append(out, Target{Item: "C9", Name: "own pod Docker TCP port", Address: net.JoinHostPort(ip, port), Expect: ExpectBlocked})
		}
	}
	return out
}
