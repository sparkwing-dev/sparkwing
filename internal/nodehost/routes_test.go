package nodehost_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
)

type entry struct {
	value  string
	masked bool
	err    error
	stall  chan error
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

func admit(t *testing.T, s *server, n routeNode) string {
	t.Helper()
	bearer, _, err := s.Admit(n)
	if err != nil {
		t.Fatal(err)
	}
	return bearer
}

func failureCode(got reply) any {
	failure, _ := got.body["error"].(map[string]any)
	return failure["code"]
}

func failureMessage(got reply) string {
	failure, _ := got.body["error"].(map[string]any)
	message, _ := failure["message"].(string)
	return message
}

type reply struct {
	status int
	body   map[string]any
}

func get(t *testing.T, s *server, path, bearer, protocol string) reply {
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
	resp, err := s.client.Do(req)
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

func secret(t *testing.T, s *server, name, bearer string) reply {
	t.Helper()
	return get(t, s, "/node/v1/secrets/"+url.PathEscape(name), bearer, authwire.NodeProtocolVersion)
}

func TestHTTPServicesNeedNoProtocolVersionHeader(t *testing.T) {
	s := start(t)
	bearer := admit(t, s, routeNode{RunID: "run", NodeID: "build", Secrets: []string{"TOKEN"}, Resolver: &fakeResolver{entries: map[string]entry{"TOKEN": {value: "value"}}}, Masker: secrets.NewMasker()})
	for _, version := range []string{"", "0", "2", "obsolete"} {
		got := get(t, s, "/node/v1/secrets/TOKEN", bearer, version)
		if got.status != http.StatusOK || got.body["value"] != "value" {
			t.Fatalf("header %q: status %d body %v", version, got.status, got.body)
		}
	}
}

func TestSecretRefusesWithoutAValidBearer(t *testing.T) {
	s := start(t)
	resolver := &fakeResolver{entries: map[string]entry{"TOKEN": {value: "s3cret-value", masked: true}}}
	bearer, revoke, err := s.Admit(routeNode{
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
	s := start(t)
	resolver := &fakeResolver{entries: map[string]entry{
		"TOKEN": {value: "declared-value", masked: true},
		"OTHER": {value: "undeclared-value", masked: true},
	}}
	bearer := admit(t, s, routeNode{
		RunID: "run-1", NodeID: "build", Secrets: []string{"TOKEN"},
		Resolver: resolver, Masker: secrets.NewMasker(),
	})
	for _, name := range []string{"OTHER", "token", "TOKEN ", "../TOKEN"} {
		got := secret(t, s, name, bearer)
		if got.status != http.StatusForbidden || failureCode(got) != "forbidden" {
			t.Fatalf("%q: status %d body %v, want 403 forbidden", name, got.status, got.body)
		}
	}
	if len(resolver.asked) != 0 {
		t.Fatalf("resolver asked for %v, undeclared names must not reach it", resolver.asked)
	}
}

func TestSecretRefusesAnotherNodesSecret(t *testing.T) {
	s := start(t)
	resolverA := &fakeResolver{entries: map[string]entry{"A_TOKEN": {value: "a-value", masked: true}}}
	resolverB := &fakeResolver{entries: map[string]entry{"B_TOKEN": {value: "b-value", masked: true}}}
	bearerA := admit(t, s, routeNode{
		RunID: "run-a", NodeID: "deploy", Secrets: []string{"A_TOKEN"},
		Resolver: resolverA, Masker: secrets.NewMasker(),
	})
	bearerB := admit(t, s, routeNode{
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
	s := start(t)
	bearerA := admit(t, s, routeNode{
		RunID: "run-a", NodeID: "build", Secrets: []string{"TOKEN"},
		Resolver: &fakeResolver{entries: map[string]entry{"TOKEN": {value: "value-for-a", masked: true}}}, Masker: secrets.NewMasker(),
	})
	bearerB := admit(t, s, routeNode{
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
	s := start(t)
	masker := secrets.NewMasker()
	bearer := admit(t, s, routeNode{
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
}

func TestSecretMissingAndFailingBackend(t *testing.T) {
	s := start(t)
	masker := secrets.NewMasker()
	masker.Register("leaked-value")
	bearer := admit(t, s, routeNode{
		RunID: "run-1", NodeID: "build", Secrets: []string{"UNSET", "BROKEN"},
		Resolver: &fakeResolver{entries: map[string]entry{
			"BROKEN": {err: errors.New("backend http://user:pw-cred@ctl echoed leaked-value")},
		}}, Masker: masker,
	})

	if got := secret(t, s, "UNSET", bearer); got.status != http.StatusNotFound || failureCode(got) != "not_found" {
		t.Fatalf("UNSET: status %d body %v, want 404 not_found", got.status, got.body)
	}
	got := secret(t, s, "BROKEN", bearer)
	if got.status != http.StatusServiceUnavailable || failureCode(got) != "unavailable" ||
		strings.Contains(failureMessage(got), "backend") {
		t.Fatalf("BROKEN: status %d body %v, want 503 unavailable without the backend's error", got.status, got.body)
	}
}

func TestAdmitRequiresABoundNode(t *testing.T) {
	s := start(t)
	full := routeNode{RunID: "run-1", NodeID: "build", Resolver: &fakeResolver{}, Masker: secrets.NewMasker()}
	for name, mutate := range map[string]func(*routeNode){
		"no run":    func(n *routeNode) { n.RunID = "" },
		"no node":   func(n *routeNode) { n.NodeID = "" },
		"no masker": func(n *routeNode) { n.Masker = nil },
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
			s := start(t)
			stall := make(chan error, 1)
			bearer, revoke, err := s.Admit(routeNode{
				RunID: "run-1", NodeID: "build", Secrets: []string{"SLOW"},
				Resolver: &fakeResolver{entries: map[string]entry{"SLOW": {stall: stall}}}, Masker: secrets.NewMasker(),
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.URL()+"/node/v1/secrets/SLOW", nil)
				req.Header.Set("Authorization", "Bearer "+bearer)
				req.Header.Set(authwire.NodeProtocolHeader, authwire.NodeProtocolVersion)
				if resp, err := s.client.Do(req); err == nil {
					_ = resp.Body.Close()
				}
			}()
			select {
			case <-stall:
			case <-ctx.Done():
				t.Fatal("secret read did not start")
			}
			if end == "revoke" {
				revoke()
			} else if err := s.Close(); err != nil {
				t.Fatalf("Close left a request in flight: %v", err)
			}
			select {
			case got := <-stall:
				if !errors.Is(got, context.Canceled) {
					t.Fatalf("stalled read ended with %v, want context.Canceled from the %s", got, end)
				}
			case <-ctx.Done():
				t.Fatal("secret read did not stop")
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("secret request did not join")
			}
		})
	}
}
