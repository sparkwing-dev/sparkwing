package cluster

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/credentials"
)

func TestBudgetBreakerTripsOnlyOnClosed(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		closed  bool
		fails   bool
	}{
		{name: "absent"},
		{name: "open", content: "open\n"},
		{name: "closed", content: "closed\n", closed: true},
		{name: "unknown", content: "half-open", fails: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if test.content != "" {
				if err := os.WriteFile(filepath.Join(dir, budgetStateCredential), []byte(test.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			creds, err := credentials.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			closed, err := budgetBreaker(creds)(t.Context())
			if closed != test.closed || (err != nil) != test.fails {
				t.Fatalf("closed=%t err=%v", closed, err)
			}
		})
	}
}

func TestBudgetBreakerRereadsTheFlag(t *testing.T) {
	dir := t.TempDir()
	creds, err := credentials.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	breaker := budgetBreaker(creds)
	if closed, err := breaker(t.Context()); closed || err != nil {
		t.Fatalf("closed=%t err=%v", closed, err)
	}
	if err := os.WriteFile(filepath.Join(dir, budgetStateCredential), []byte("closed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if closed, err := breaker(t.Context()); !closed || err != nil {
		t.Fatalf("closed=%t err=%v", closed, err)
	}
}
