package sparkwinglogs_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the CLI reads logs through this store, and without a process
// identity every CLI invocation on a host counts against the token the
// whole team shares.
func TestStoreSendsTheProcessIdentityOnReads(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get(store.RunnerIdentityHeader))
		mu.Unlock()
		_, _ = w.Write([]byte("line\n"))
	}))
	t.Cleanup(srv.Close)

	identity := logs.ProcessIdentity("cli")
	s := sparkwinglogs.New(srv.URL, nil, "").WithRunnerIdentity(identity)
	if _, err := s.Read(t.Context(), "r1", "n1", storage.ReadOpts{}); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, err := s.ReadRun(t.Context(), "r1"); err != nil {
		t.Fatalf("ReadRun: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("saw %d requests, want the two reads", len(seen))
	}
	for _, got := range seen {
		if got != identity {
			t.Errorf("read carried identity %q, want %q", got, identity)
		}
	}
}

// safety: the identity must be stable for the life of the process, because
// one that changed per request would hand the service a fresh budget on
// every poll and grow its bucket table at the caller's request rate.
func TestProcessIdentityIsStableAndNamesThisProcess(t *testing.T) {
	first, second := logs.ProcessIdentity("cli"), logs.ProcessIdentity("cli")
	if first != second {
		t.Fatalf("ProcessIdentity returned %q then %q; it must be stable", first, second)
	}
	parts := strings.Split(first, ":")
	if len(parts) != 3 {
		t.Fatalf("identity %q is not role:host:pid", first)
	}
	if parts[0] != "cli" {
		t.Errorf("role = %q, want cli", parts[0])
	}
	if host, err := os.Hostname(); err == nil && host != "" && parts[1] != host {
		t.Errorf("host = %q, want %q", parts[1], host)
	}
	if parts[2] != strconv.Itoa(os.Getpid()) {
		t.Errorf("pid = %q, want %d", parts[2], os.Getpid())
	}
	if other := logs.ProcessIdentity("runner"); other == first {
		t.Error("two roles produced one identity")
	}
}

// safety: a store nobody named still reads, it just shares the token's
// budget; the identity is an improvement, never a requirement.
func TestAStoreWithNoIdentityStillReads(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get(store.RunnerIdentityHeader)
		_, _ = w.Write([]byte("line\n"))
	}))
	t.Cleanup(srv.Close)

	if _, err := sparkwinglogs.New(srv.URL, nil, "").Read(t.Context(), "r1", "n1", storage.ReadOpts{}); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if seen != "" {
		t.Fatalf("an unnamed store sent identity %q", seen)
	}
}
