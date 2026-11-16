package isolationprobe

import (
	"context"
	"errors"
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
	open.lookup = func(context.Context, string) ([]string, error) { return []string{"192.0.2.1"}, nil }
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
	broken := newNetwork()
	broken.lookup = func(context.Context, string) ([]string, error) {
		return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
	}
	broken.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, timeoutErr{} })}
	broken.dial = func(context.Context, string, string) (net.Conn, error) { return nil, dialErr(syscall.ENETUNREACH) }
	results := broken.positive(t.Context(), ContextJob)
	if len(results) != 3 {
		t.Fatalf("positive controls = %d, want 3", len(results))
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
		host := "10.0.0." + strconv.Itoa(i)
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

func TestOwnNodeTargetsGradeOnlyTheKubelet(t *testing.T) {
	if got := ownNodeTargets(""); got != nil {
		t.Fatalf("no node gives %v, want none", got)
	}
	graded := 0
	for _, tg := range ownNodeTargets("10.0.51.209") {
		if err := tg.validate(); err != nil {
			t.Errorf("%s: %v", tg.Name, err)
		}
		if tg.Expect == ExpectDenied {
			graded++
			if tg.Grade != GradeKubelet || tg.Address != "10.0.51.209:10250" {
				t.Errorf("graded own-node target %+v, want the kubelet", tg)
			}
		} else if tg.Expect != ExpectObserved {
			t.Errorf("%s expects %s, want observed", tg.Name, tg.Expect)
		}
	}
	if graded != 1 {
		t.Fatalf("graded own-node targets = %d, want 1", graded)
	}
}
