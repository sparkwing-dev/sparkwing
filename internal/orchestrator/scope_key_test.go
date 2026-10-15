package orchestrator

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestScopeKeySeparatesScopes(t *testing.T) {
	global := nodehost.ConcurrencyKey(nodeproto.ConcurrencySpec{Group: "collide@advbox-host"}, "run-1", "advbox-host")
	box := nodehost.ConcurrencyKey(nodeproto.ConcurrencySpec{Group: "collide", Scope: new("box")}, "run-1", "advbox-host")
	if global == box {
		t.Fatalf("global and box keys collide: %q", global)
	}
	for _, tc := range []struct{ scope, name, want string }{
		{"global", "payments@db", "global"},
		{"box", "db", "box (host1)"},
		{"run", "db", "run (run-7)"},
	} {
		key := nodehost.ConcurrencyKey(nodeproto.ConcurrencySpec{Group: tc.name, Scope: new(tc.scope)}, "run-7", "host1")
		if got := ScopeLabelFromKey(key); got != tc.want {
			t.Errorf("label of %q = %q, want %q", key, got, tc.want)
		}
	}
}

func TestScopeKeyLabelRecoversQualifierWithSeparators(t *testing.T) {
	for _, tc := range []struct{ scope, want string }{
		{"box", "box (host:with@seps)"},
		{"run", "run (run:1@x)"},
	} {
		key := nodehost.ConcurrencyKey(nodeproto.ConcurrencySpec{Group: "db", Scope: new(tc.scope)}, "run:1@x", "host:with@seps")
		if got := ScopeLabelFromKey(key); got != tc.want {
			t.Errorf("label of %q = %q, want %q", key, got, tc.want)
		}
	}
}

func FuzzConcurrencyKeyPreservesIdentity(f *testing.F) {
	for _, seed := range [][4]string{{"a", "@b", "a@", "b"}, {"a", ":b", "a:", "b"}, {"1", ":x", "1:", "x"}} {
		f.Add(seed[0], seed[1], seed[2], seed[3])
	}
	f.Fuzz(func(t *testing.T, qA, nA, qB, nB string) {
		if nA == "" || nB == "" || qA == qB && nA == nB {
			return
		}
		for _, scope := range []string{"run", "box"} {
			a := nodehost.ConcurrencyKey(nodeproto.ConcurrencySpec{Group: nA, Scope: new(scope)}, qA, qA)
			b := nodehost.ConcurrencyKey(nodeproto.ConcurrencySpec{Group: nB, Scope: new(scope)}, qB, qB)
			if a == b {
				t.Fatalf("%s keys alias (%q,%q) and (%q,%q): %q", scope, qA, nA, qB, nB, a)
			}
		}
	})
}
