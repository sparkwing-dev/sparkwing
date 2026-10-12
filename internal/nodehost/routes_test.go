package nodehost_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
)

type entry struct {
	value  string
	masked bool
	err    error
	// hack: stall blocks the read until its context ends, then reports why.
	stall chan error
}

type fakeResolver struct {
	mu      sync.Mutex
	entries map[string]entry
	asked   []string
}

func (f *fakeResolver) Resolve(ctx context.Context, name string) (string, bool, error) {
	f.mu.Lock()
	f.asked = append(f.asked, name)
	e, ok := f.entries[name]
	f.mu.Unlock()
	if !ok {
		return "", false, secretsource.ErrMissing
	}
	if e.stall != nil {
		e.stall <- nil
		<-ctx.Done()
		e.stall <- ctx.Err()
		return "", false, ctx.Err()
	}
	return e.value, e.masked, e.err
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func start(t *testing.T) (*nodehost.Server, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	s, err := nodehost.Start(slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, logs
}

func admit(t *testing.T, s *nodehost.Server, n nodehost.Node) string {
	t.Helper()
	bearer, _, err := s.Admit(n)
	if err != nil {
		t.Fatal(err)
	}
	return bearer
}

type reply struct {
	status int
	body   map[string]any
}

func get(t *testing.T, s *nodehost.Server, path, bearer, protocol string) reply {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.URL()+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if protocol != "" {
		req.Header.Set(authwire.NodeProtocolHeader, protocol)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := reply{status: resp.StatusCode}
	if err := json.NewDecoder(resp.Body).Decode(&out.body); err != nil {
		t.Fatalf("%s: decode reply: %v", path, err)
	}
	return out
}

func secret(t *testing.T, s *nodehost.Server, name, bearer string) reply {
	t.Helper()
	return get(t, s, "/node/v1/secrets/"+url.PathEscape(name), bearer, authwire.NodeProtocolVersion)
}

func TestServerListensOnLoopbackOnly(t *testing.T) {
	s, _ := start(t)
	u, err := url.Parse(s.URL())
	if err != nil {
		t.Fatal(err)
	}
	if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() {
		t.Fatalf("server listens on %q, want a loopback address", u.Host)
	}
}

func TestProtocolMismatchAnswers426WithSupportedVersion(t *testing.T) {
	s, _ := start(t)
	bearer := admit(t, s, nodehost.Node{
		RunID: "run-1", NodeID: "build", Secrets: []string{"TOKEN"},
		Resolver: &fakeResolver{}, Masker: secrets.NewMasker(),
	})
	for _, protocol := range []string{"", "0", "2", "1.0", strings.Repeat("\x80", 4<<10)} {
		got := get(t, s, "/node/v1/secrets/TOKEN", bearer, protocol)
		if got.status != http.StatusUpgradeRequired {
			t.Fatalf("protocol %q: status %d, want 426", protocol, got.status)
		}
		if got.body["error"] != "node protocol unsupported" || got.body["code"] != "protocol_mismatch" {
			t.Fatalf("protocol %q: body %v, want the fixed message and protocol_mismatch", protocol[:min(len(protocol), 8)], got.body)
		}
		supported, _ := got.body["supported"].([]any)
		if len(supported) != 1 || supported[0] != authwire.NodeProtocolVersion {
			t.Fatalf("protocol %q: supported = %v, want [%s]", protocol, got.body["supported"], authwire.NodeProtocolVersion)
		}
	}
}

func TestSecretRefusesWithoutAValidBearer(t *testing.T) {
	s, _ := start(t)
	resolver := &fakeResolver{entries: map[string]entry{"TOKEN": {value: "s3cret-value", masked: true}}}
	bearer, revoke, err := s.Admit(nodehost.Node{
		RunID: "run-1", NodeID: "build", Secrets: []string{"TOKEN"},
		Resolver: resolver, Masker: secrets.NewMasker(),
	})
	if err != nil {
		t.Fatal(err)
	}
	forged := strings.Repeat("A", len(bearer))
	for _, b := range []string{"", forged, bearer[:len(bearer)-1], bearer + "x"} {
		if got := secret(t, s, "TOKEN", b); got.status != http.StatusUnauthorized {
			t.Fatalf("bearer %q: status %d, want 401", b, got.status)
		}
	}
	revoke()
	if got := secret(t, s, "TOKEN", bearer); got.status != http.StatusUnauthorized {
		t.Fatalf("revoked bearer: status %d, want 401", got.status)
	}
	if len(resolver.asked) != 0 {
		t.Fatalf("resolver asked for %v without a valid bearer", resolver.asked)
	}
}

func TestSecretRefusesANameTheNodeDidNotDeclare(t *testing.T) {
	s, _ := start(t)
	resolver := &fakeResolver{entries: map[string]entry{
		"TOKEN": {value: "declared-value", masked: true},
		"OTHER": {value: "undeclared-value", masked: true},
	}}
	bearer := admit(t, s, nodehost.Node{
		RunID: "run-1", NodeID: "build", Secrets: []string{"TOKEN"},
		Resolver: resolver, Masker: secrets.NewMasker(),
	})
	for _, name := range []string{"OTHER", "token", "TOKEN ", "../TOKEN"} {
		got := secret(t, s, name, bearer)
		if got.status != http.StatusForbidden || got.body["code"] != "secret_undeclared" {
			t.Fatalf("%q: status %d body %v, want 403 secret_undeclared", name, got.status, got.body)
		}
	}
	if len(resolver.asked) != 0 {
		t.Fatalf("resolver asked for %v, undeclared names must not reach it", resolver.asked)
	}
}

func TestSecretRefusesAnotherNodesSecret(t *testing.T) {
	s, _ := start(t)
	resolverA := &fakeResolver{entries: map[string]entry{"A_TOKEN": {value: "a-value", masked: true}}}
	resolverB := &fakeResolver{entries: map[string]entry{"B_TOKEN": {value: "b-value", masked: true}}}
	bearerA := admit(t, s, nodehost.Node{
		RunID: "run-a", NodeID: "deploy", Secrets: []string{"A_TOKEN"},
		Resolver: resolverA, Masker: secrets.NewMasker(),
	})
	bearerB := admit(t, s, nodehost.Node{
		RunID: "run-b", NodeID: "deploy", Secrets: []string{"B_TOKEN"},
		Resolver: resolverB, Masker: secrets.NewMasker(),
	})

	if got := secret(t, s, "A_TOKEN", bearerB); got.status != http.StatusForbidden {
		t.Fatalf("node B read node A's secret: status %d body %v, want 403", got.status, got.body)
	}
	if got := secret(t, s, "B_TOKEN", bearerA); got.status != http.StatusForbidden {
		t.Fatalf("node A read node B's secret: status %d body %v, want 403", got.status, got.body)
	}
	if got := secret(t, s, "A_TOKEN", bearerA); got.status != http.StatusOK || got.body["value"] != "a-value" {
		t.Fatalf("node A's own secret: status %d body %v", got.status, got.body)
	}
	if len(resolverB.asked) != 0 {
		t.Fatalf("node B's resolver asked for %v by node A's request", resolverB.asked)
	}
}

func TestSecretResolvesThroughTheNodesOwnResolver(t *testing.T) {
	s, _ := start(t)
	bearerA := admit(t, s, nodehost.Node{
		RunID: "run-a", NodeID: "build", Secrets: []string{"TOKEN"},
		Resolver: &fakeResolver{entries: map[string]entry{"TOKEN": {value: "value-for-a", masked: true}}}, Masker: secrets.NewMasker(),
	})
	bearerB := admit(t, s, nodehost.Node{
		RunID: "run-b", NodeID: "build", Secrets: []string{"TOKEN"},
		Resolver: &fakeResolver{entries: map[string]entry{"TOKEN": {value: "value-for-b", masked: true}}}, Masker: secrets.NewMasker(),
	})
	if got := secret(t, s, "TOKEN", bearerA); got.body["value"] != "value-for-a" {
		t.Fatalf("node A got %v", got.body)
	}
	if got := secret(t, s, "TOKEN", bearerB); got.body["value"] != "value-for-b" {
		t.Fatalf("node B got %v", got.body)
	}
}

func TestSecretRegistersAMaskedValueBeforeAnswering(t *testing.T) {
	s, logs := start(t)
	masker := secrets.NewMasker()
	bearer := admit(t, s, nodehost.Node{
		RunID: "run-1", NodeID: "build", Secrets: []string{"TOKEN", "REGION"},
		Resolver: &fakeResolver{entries: map[string]entry{
			"TOKEN":  {value: "s3cret-value", masked: true},
			"REGION": {value: "us-west-2", masked: false},
		}}, Masker: masker,
	})

	got := secret(t, s, "TOKEN", bearer)
	if got.status != http.StatusOK || got.body["value"] != "s3cret-value" || got.body["masked"] != true {
		t.Fatalf("TOKEN: status %d body %v", got.status, got.body)
	}
	if strings.Contains(masker.Mask("log s3cret-value"), "s3cret-value") {
		t.Fatal("masked secret was answered before the masker held it")
	}
	got = secret(t, s, "REGION", bearer)
	if got.status != http.StatusOK || got.body["value"] != "us-west-2" || got.body["masked"] != false {
		t.Fatalf("REGION: status %d body %v", got.status, got.body)
	}
	if masker.Mask("us-west-2") != "us-west-2" {
		t.Fatal("an unmasked config value was registered with the masker")
	}
	if strings.Contains(logs.String(), bearer) || strings.Contains(logs.String(), "s3cret-value") {
		t.Fatalf("logs leak the bearer or a secret value:\n%s", logs.String())
	}
}

func TestSecretMissingAndFailingBackend(t *testing.T) {
	s, logs := start(t)
	masker := secrets.NewMasker()
	masker.Register("leaked-value")
	bearer := admit(t, s, nodehost.Node{
		RunID: "run-1", NodeID: "build", Secrets: []string{"UNSET", "BROKEN"},
		Resolver: &fakeResolver{entries: map[string]entry{
			"BROKEN": {err: errors.New("backend http://user:pw-cred@ctl echoed leaked-value")},
		}}, Masker: masker,
	})

	if got := secret(t, s, "UNSET", bearer); got.status != http.StatusNotFound || got.body["code"] != "secret_not_found" {
		t.Fatalf("UNSET: status %d body %v, want 404 secret_not_found", got.status, got.body)
	}
	got := secret(t, s, "BROKEN", bearer)
	if got.status != http.StatusBadGateway || got.body["code"] != "secret_unavailable" ||
		strings.Contains(got.body["error"].(string), "backend") {
		t.Fatalf("BROKEN: status %d body %v, want 502 secret_unavailable without the backend's error", got.status, got.body)
	}
	if l := logs.String(); strings.Contains(l, "backend") || strings.Contains(l, "pw-cred") || strings.Contains(l, bearer) {
		t.Fatalf("logs leak the bearer or a secret value:\n%s", logs.String())
	}
}

func TestAdmitRequiresABoundNode(t *testing.T) {
	s, _ := start(t)
	full := nodehost.Node{RunID: "run-1", NodeID: "build", Resolver: &fakeResolver{}, Masker: secrets.NewMasker()}
	for name, mutate := range map[string]func(*nodehost.Node){
		"no run":      func(n *nodehost.Node) { n.RunID = "" },
		"no node":     func(n *nodehost.Node) { n.NodeID = "" },
		"no resolver": func(n *nodehost.Node) { n.Resolver = nil },
		"no masker":   func(n *nodehost.Node) { n.Masker = nil },
	} {
		n := full
		mutate(&n)
		if _, _, err := s.Admit(n); err == nil {
			t.Fatalf("%s: Admit succeeded", name)
		}
	}
	a, _, _ := s.Admit(full)
	b, _, _ := s.Admit(full)
	if a == b {
		t.Fatal("two admissions of the same node share a bearer")
	}
}

func TestStalledSecretReadEndsOnRevokeAndClose(t *testing.T) {
	for _, end := range []string{"revoke", "close"} {
		t.Run(end, func(t *testing.T) {
			s, _ := start(t)
			stall := make(chan error, 1)
			bearer, revoke, err := s.Admit(nodehost.Node{
				RunID: "run-1", NodeID: "build", Secrets: []string{"SLOW"},
				Resolver: &fakeResolver{entries: map[string]entry{"SLOW": {stall: stall}}}, Masker: secrets.NewMasker(),
			})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				req, _ := http.NewRequest(http.MethodGet, s.URL()+"/node/v1/secrets/SLOW", nil)
				req.Header.Set("Authorization", "Bearer "+bearer)
				req.Header.Set(authwire.NodeProtocolHeader, authwire.NodeProtocolVersion)
				if resp, err := http.DefaultClient.Do(req); err == nil {
					_ = resp.Body.Close()
				}
			}()
			<-stall
			if end == "revoke" {
				revoke()
			} else if err := s.Close(); err != nil {
				t.Fatalf("Close left a request in flight: %v", err)
			}
			if got := <-stall; !errors.Is(got, context.Canceled) {
				t.Fatalf("stalled read ended with %v, want context.Canceled from the %s", got, end)
			}
			<-done
		})
	}
}
