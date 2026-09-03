package wingd_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestTerminalCheckEvictionCarriesTheStoreFailureVerbatim(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home:    home,
		Version: "v0.38.2",
		Runs: &wingd.FuncRunStore{IsTerminal: func(string) (bool, error) {
			return false, errors.New("database is at schema version 26; this binary expects 17")
		}},
	})
	cl := ensure(t, home, "v0.38.2")

	_, err := cl.Acquire(context.Background(), coreReq("schema-skew", 1), nil)
	var admErr *client.AdmissionError
	if !errors.As(err, &admErr) {
		t.Fatalf("acquire error = %v, want an admission error", err)
	}
	if admErr.Key != "terminal-check" {
		t.Fatalf("eviction key = %q, want terminal-check", admErr.Key)
	}
	if !strings.Contains(admErr.Reason, "database is at schema version 26; this binary expects 17") {
		t.Fatalf("eviction reason = %q, want the store failure verbatim", admErr.Reason)
	}
	if !strings.Contains(admErr.Reason, "v0.38.2") {
		t.Fatalf("eviction reason = %q, want the daemon version named", admErr.Reason)
	}
}

func TestHandshakeAdvertisesTheDaemonStoreSchema(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, StoreSchemaVersion: 17})

	cl := ensure(t, home, "")
	if got := cl.DaemonStoreSchema(); got != 17 {
		t.Fatalf("advertised store schema = %d, want 17", got)
	}

	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	info, err := client.Probe(context.Background(), sock)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if info.StoreSchemaVersion != 17 {
		t.Fatalf("probed store schema = %d, want 17", info.StoreSchemaVersion)
	}
}
