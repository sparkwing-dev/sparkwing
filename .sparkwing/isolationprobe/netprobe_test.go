package isolationprobe

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func dialErr(errno syscall.Errno) error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)}
}

func TestClassifyDialNamesWhatTheNetworkDid(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want string
	}{
		"connected":                     {nil, outcomeConnected},
		"refused":                       {dialErr(syscall.ECONNREFUSED), outcomeRefused},
		"host unreachable":              {dialErr(syscall.EHOSTUNREACH), outcomeHostUnreachable},
		"network unreachable":           {dialErr(syscall.ENETUNREACH), outcomeNetUnreachable},
		"denied by the local stack":     {dialErr(syscall.EPERM), outcomeLocalDenied},
		"no IPv6 address":               {dialErr(syscall.EADDRNOTAVAIL), outcomeNoAddress},
		"a dropped SYN":                 {&net.OpError{Op: "dial", Net: "tcp", Err: timeoutErr{}}, outcomeTimeout},
		"the probe's own deadline":      {context.DeadlineExceeded, outcomeTimeout},
		"a DNS timeout is DNS":          {&net.OpError{Op: "dial", Err: &net.DNSError{Err: "i/o timeout", Name: "x", IsTimeout: true}}, outcomeDNSFailure},
		"a TLS failure proves nothing":  {errors.New("tls: handshake failure"), outcomeError},
		"a missing name proves nothing": {&net.DNSError{Err: "no such host", Name: "x", IsNotFound: true}, outcomeDNSFailure},
	} {
		if got := classifyDial(c.err); got != c.want {
			t.Errorf("%s: classifyDial = %q, want %q", name, got, c.want)
		}
	}
}

func TestDialVerdictPassesOnlyTheFencesOwnFailureModes(t *testing.T) {
	blocked := Target{Expect: ExpectBlocked}
	listening := Target{Expect: ExpectBlocked, Listening: true}
	for name, c := range map[string]struct {
		target  Target
		outcome string
		want    Verdict
	}{
		"dropped":                          {listening, outcomeTimeout, Pass},
		"rejected":                         {listening, outcomeHostUnreachable, Pass},
		"no route":                         {listening, outcomeNetUnreachable, Pass},
		"no IPv6 address":                  {blocked, outcomeNoAddress, Pass},
		"connected":                        {blocked, outcomeConnected, Breach},
		"refused by a known listener":      {listening, outcomeRefused, Breach},
		"refused where nothing listens":    {blocked, outcomeRefused, Pass},
		"DNS failure":                      {blocked, outcomeDNSFailure, Inconclusive},
		"unclassified error":               {blocked, outcomeError, Inconclusive},
		"denied target awaits its grade":   {Target{Expect: ExpectDenied, Grade: GradeKubelet}, outcomeConnected, Inconclusive},
		"positive control reached":         {Target{Expect: ExpectOpen}, outcomeConnected, Pass},
		"positive control missed":          {Target{Expect: ExpectOpen}, outcomeTimeout, Inconclusive},
		"observed target is never judged":  {Target{Expect: ExpectObserved}, outcomeConnected, Observed},
		"observed target dropped is noted": {Target{Expect: ExpectObserved}, outcomeTimeout, Observed},
	} {
		if got := dialVerdict(c.target, c.outcome); got != c.want {
			t.Errorf("%s: dialVerdict = %s, want %s", name, got, c.want)
		}
	}
}

func TestGradeVerdictPassesOnlyAnonymousRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		codes []int
		err   error
		want  Verdict
	}{
		"unauthorized and forbidden": {[]int{401, 403}, nil, Pass},
		"one success":                {[]int{401, 200}, nil, Breach},
		"created":                    {[]int{201}, nil, Breach},
		"not found proves nothing":   {[]int{404}, nil, Inconclusive},
		"no answer":                  {nil, errors.New("timeout"), Inconclusive},
		"a partial answer":           {[]int{401}, errors.New("timeout"), Inconclusive},
		"nothing at all":             {nil, nil, Inconclusive},
	} {
		if got := gradeVerdict(c.codes, c.err); got != c.want {
			t.Errorf("%s: gradeVerdict = %s, want %s", name, got, c.want)
		}
	}
}

func listener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// A fenced target the job can reach, or one that refuses where something is known to listen, is a
// breach; a refusal where nothing listens is the fence's.
func TestProbeJudgesARealConnection(t *testing.T) {
	ln := listener(t)
	n := newNetwork()
	addr := ln.Addr().String()
	if r := n.probe(t.Context(), ContextJob, Target{Item: "C6", Name: "controller pod", Address: addr, Expect: ExpectBlocked, Listening: true}); r.Verdict != Breach || r.Outcome != outcomeConnected {
		t.Fatalf("reachable fenced target = %+v, want a connected breach", r)
	}
	_ = ln.Close()
	if r := n.probe(t.Context(), ContextJob, Target{Item: "C6", Name: "controller pod", Address: addr, Expect: ExpectBlocked, Listening: true}); r.Verdict != Breach || r.Outcome != outcomeRefused {
		t.Fatalf("refusal at a known listener = %+v, want a refused breach", r)
	}
	if r := n.probe(t.Context(), ContextJob, Target{Item: "C6", Name: "closed port", Address: addr, Expect: ExpectBlocked}); r.Verdict != Pass {
		t.Fatalf("refusal where nothing listens = %+v, want pass", r)
	}
}

func TestProbeTreatsADroppedConnectionAsTheFence(t *testing.T) {
	n := newNetwork()
	n.dial = func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: timeoutErr{}}
	}
	r := n.probe(t.Context(), ContextJob, Target{Item: "C4", Name: "IMDS", Address: "169.254.169.254:80", Expect: ExpectBlocked, Listening: true, Grade: GradeIMDS})
	if r.Verdict != Pass || r.Outcome != outcomeTimeout {
		t.Fatalf("dropped IMDS = %+v, want a timeout pass", r)
	}
}

func TestDeniedTargetIsSettledByItsAnonymousAnswer(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		want   Verdict
	}{
		"refuses":  {http.StatusUnauthorized, Pass},
		"answers":  {http.StatusOK, Breach},
		"confuses": {http.StatusNotFound, Inconclusive},
	} {
		var auth []string
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth = append(auth, r.Header.Get("Authorization"))
			if r.URL.Path != "/pods" {
				t.Errorf("%s: kubelet grade asked for %s", name, r.URL.Path)
			}
			w.WriteHeader(c.status)
		}))
		r := newNetwork().probe(t.Context(), ContextJob, Target{Item: "C8", Name: "own node kubelet", Address: srv.Listener.Addr().String(), Expect: ExpectDenied, Listening: true, Grade: GradeKubelet})
		srv.Close()
		if r.Verdict != c.want || !strings.HasPrefix(r.Outcome, "connected; http-"+strconv.Itoa(c.status)) {
			t.Errorf("%s: %+v, want %s after http-%d", name, r, c.want, c.status)
		}
		if len(auth) != 1 || auth[0] != "" {
			t.Errorf("%s: grade requests carried credentials: %q", name, auth)
		}
	}
}

// A fenced target stays a breach even when its anonymous answer is a refusal; the answer is
// recorded so the report can say which control held.
func TestBlockedTargetThatAnswersStaysABreachWithItsAnswerRecorded(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	r := newNetwork().probe(t.Context(), ContextJob, Target{Item: "C4", Name: "IMDS", Address: srv.Listener.Addr().String(), Expect: ExpectBlocked, Listening: true, Grade: GradeIMDS})
	if r.Verdict != Breach || r.Outcome != "connected; http-401,http-401" {
		t.Fatalf("answered IMDS = %+v, want a breach recording both refusals", r)
	}
	if strings.Join(methods, ",") != "PUT /latest/api/token,GET /latest/meta-data/" {
		t.Fatalf("IMDS grade requests = %v", methods)
	}
}

func TestPositiveControlsNeedTheWholePublicPath(t *testing.T) {
	open := newNetwork()
	var looked []string
	open.lookup = func(_ context.Context, host string) ([]string, error) {
		looked = append(looked, host)
		return []string{"192.0.2.1"}, nil
	}
	open.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusMovedPermanently, Body: http.NoBody}, nil
	})}
	open.dial = func(context.Context, string, string) (net.Conn, error) {
		c1, c2 := net.Pipe()
		_ = c2.Close()
		return c1, nil
	}
	for _, r := range open.positive(t.Context(), ContextBridge) {
		if r.Verdict != Pass || r.Context != ContextBridge {
			t.Errorf("working network: %+v, want pass", r)
		}
	}
	if strings.Join(looked, ",") != "github.com,kubernetes.default.svc.cluster.local" {
		t.Fatalf("lookups = %v, want a public name and the cluster's own", looked)
	}
	broken := newNetwork()
	broken.lookup = func(context.Context, string) ([]string, error) {
		return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
	}
	broken.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, timeoutErr{} })}
	broken.dial = func(context.Context, string, string) (net.Conn, error) { return nil, dialErr(syscall.ENETUNREACH) }
	results := broken.positive(t.Context(), ContextJob)
	if len(results) != len(positiveChecks) {
		t.Fatalf("positive controls = %d, want %d", len(results), len(positiveChecks))
	}
	for _, r := range results {
		if r.Verdict != Inconclusive {
			t.Errorf("broken network: %+v, want inconclusive", r)
		}
	}
}

func TestSelfConnectProvesTheListenerPeersAreMeasuredAgainst(t *testing.T) {
	ln, err := listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	n := newNetwork()
	if r := n.selfConnect(t.Context(), ContextJob, []string{"127.0.0.1"}, port); len(r) != 1 || r[0].Verdict != Pass {
		t.Fatalf("self-connect = %+v, want pass", r)
	}
	if r := n.selfConnect(t.Context(), ContextJob, nil, port); len(r) != 1 || r[0].Verdict != Inconclusive {
		t.Fatalf("self-connect without a pod address = %+v, want inconclusive", r)
	}
}

func TestProbeAllKeepsTargetOrder(t *testing.T) {
	n := newNetwork()
	n.parallel = 2
	n.dial = func(_ context.Context, _, addr string) (net.Conn, error) {
		if strings.HasPrefix(addr, "10.") {
			return nil, &net.OpError{Op: "dial", Err: timeoutErr{}}
		}
		return nil, dialErr(syscall.ECONNREFUSED)
	}
	var targets []Target
	for i := range 9 {
		host := "10.9.0." + strconv.Itoa(i)
		if i%2 == 1 {
			host = "192.0.2." + strconv.Itoa(i)
		}
		targets = append(targets, Target{Item: "C6", Name: host, Address: host + ":443", Expect: ExpectBlocked})
	}
	for i, r := range n.probeAll(t.Context(), ContextJob, targets) {
		if r.Probe != targets[i].Name {
			t.Fatalf("result %d is %s, want %s", i, r.Probe, targets[i].Name)
		}
	}
}

// The own node's kubelet may answer only to refuse; every other port on the own node must stay out
// of reach, the read-only kubelet and kube-proxy metrics included.
func TestOwnNodeTargetsJudgeEveryPort(t *testing.T) {
	if got := ownNodeTargets(""); got != nil {
		t.Fatalf("no node gives %v, want none", got)
	}
	ports := map[string]Expect{}
	for _, tg := range ownNodeTargets("10.9.1.1") {
		if err := tg.validate(); err != nil {
			t.Errorf("%s: %v", tg.Name, err)
		}
		_, port, _ := net.SplitHostPort(tg.Address)
		ports[port] = tg.Expect
		if tg.Expect == ExpectDenied && tg.Grade != GradeKubelet {
			t.Errorf("graded own-node target %+v, want the kubelet", tg)
		}
	}
	want := map[string]Expect{"10250": ExpectDenied, "10255": ExpectBlocked, "10249": ExpectBlocked, "10256": ExpectBlocked, "22": ExpectBlocked, "61678": ExpectBlocked, "61679": ExpectBlocked, "61680": ExpectBlocked}
	if len(ports) != len(want) {
		t.Fatalf("own-node ports = %v, want %v", ports, want)
	}
	for port, expect := range want {
		if ports[port] != expect {
			t.Errorf("own-node port %s expects %q, want %q", port, ports[port], expect)
		}
	}
}

func TestReachableOwnReadOnlyKubeletFails(t *testing.T) {
	ln := listener(t)
	for _, tg := range ownNodeTargets("192.0.2.1") {
		if strings.HasSuffix(tg.Address, ":10255") {
			tg.Address = ln.Addr().String()
			if r := newNetwork().probe(t.Context(), ContextJob, tg); r.Verdict != Breach {
				t.Fatalf("reachable read-only kubelet = %+v, want breach", r)
			}
			return
		}
	}
	t.Fatal("no read-only kubelet target")
}

func TestOwnPodDockerTargetsCoverLoopbackAndPodAddresses(t *testing.T) {
	var got []string
	for _, tg := range ownPodDockerTargets([]string{"10.9.2.2"}) {
		if tg.Expect != ExpectBlocked || tg.Listening || tg.Item != "C9" {
			t.Errorf("own pod Docker target %+v", tg)
		}
		got = append(got, tg.Address)
	}
	if strings.Join(got, ",") != "127.0.0.1:2375,127.0.0.1:2376,10.9.2.2:2375,10.9.2.2:2376" {
		t.Fatalf("own pod Docker targets = %v", got)
	}
}

// The kube API grade asks about the deployment's own namespace: its secrets, the caller's rules
// there and a dry-run Job that mounts a secret. All three must be refused.
func TestKubeAPIGradeAsksAboutTheDeploymentsNamespace(t *testing.T) {
	var requests []string
	status := http.StatusForbidden
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		if r.Header.Get("Authorization") != "" {
			t.Errorf("grade request carried a credential")
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	target := Target{Item: "C7", Name: "kube API public endpoint", Address: srv.Listener.Addr().String(), Expect: ExpectDenied, Listening: true, Grade: GradeKubeAPI, Namespace: "staging-ns"}
	r := newNetwork().probe(t.Context(), ContextJob, target)
	if r.Verdict != Pass || r.Outcome != "connected; http-403,http-403,http-403" {
		t.Fatalf("refused kube API = %+v, want pass on three refusals", r)
	}
	if len(requests) != 3 ||
		!strings.HasPrefix(requests[0], "GET /api/v1/namespaces/staging-ns/secrets ") ||
		!strings.HasPrefix(requests[1], "POST /apis/authorization.k8s.io/v1/selfsubjectrulesreviews ") || !strings.Contains(requests[1], `"namespace":"staging-ns"`) ||
		!strings.HasPrefix(requests[2], "POST /apis/batch/v1/namespaces/staging-ns/jobs?dryRun=All ") || !strings.Contains(requests[2], `"secretName"`) {
		t.Fatalf("kube API grade requests = %q", requests)
	}
	requests, status = nil, http.StatusCreated
	if r := newNetwork().probe(t.Context(), ContextJob, target); r.Verdict != Breach {
		t.Fatalf("admitted dry-run Job = %+v, want breach", r)
	}
	target.Namespace = ""
	if _, err := newNetwork().grade(t.Context(), target); err == nil {
		t.Fatal("a kube API grade ran without the namespace it asks about")
	}
}
