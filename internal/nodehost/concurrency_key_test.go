package nodehost

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestParseConcurrencyKeyRefusesMalformedTuples(t *testing.T) {
	for _, key := range []string{"", "db", "other:db", "run:id:db", "run:-1:id:db", "box:+1:a:db", "run:01:a:db", "run:1:", "box:2:a:db", "run:999999999999999999999999:x:db"} {
		if _, _, _, ok := ParseConcurrencyKey(key); ok {
			t.Errorf("accepted malformed key %q", key)
		}
	}
}

func FuzzConcurrencyKeyRoundTrip(f *testing.F) {
	for _, seed := range [][2]string{{"host:with@seps", "db"}, {"", ""}, {"1:db", ":other"}, {"界\x00\xff", "\xff\x00:"}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, identity, group string) {
		keys := map[string]bool{}
		for _, scope := range []string{"global", "run", "box"} {
			key := ConcurrencyKey(nodeproto.ConcurrencySpec{Scope: new(scope), Group: group}, identity, identity)
			gotScope, gotIdentity, gotGroup, ok := ParseConcurrencyKey(key)
			wantIdentity := identity
			if scope == "global" {
				wantIdentity = ""
			}
			if !ok || gotScope != scope || gotIdentity != wantIdentity || gotGroup != group || keys[key] {
				t.Fatalf("tuple (%q,%q,%q) encoded as %q and decoded as (%q,%q,%q,%t)", scope, wantIdentity, group, key, gotScope, gotIdentity, gotGroup, ok)
			}
			keys[key] = true
		}
	})
}

func TestScopeKeySeparatesScopes(t *testing.T) {
	global := ConcurrencyKey(nodeproto.ConcurrencySpec{Group: "collide@advbox-host"}, "run-1", "advbox-host")
	box := ConcurrencyKey(nodeproto.ConcurrencySpec{Group: "collide", Scope: new("box")}, "run-1", "advbox-host")
	if global == box {
		t.Fatalf("global and box keys collide: %q", global)
	}
	for _, tc := range []struct{ scope, name, want string }{
		{"global", "payments@db", "global"},
		{"box", "db", "box (host1)"},
		{"run", "db", "run (run-7)"},
	} {
		key := ConcurrencyKey(nodeproto.ConcurrencySpec{Group: tc.name, Scope: new(tc.scope)}, "run-7", "host1")
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
		key := ConcurrencyKey(nodeproto.ConcurrencySpec{Group: "db", Scope: new(tc.scope)}, "run:1@x", "host:with@seps")
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
			a := ConcurrencyKey(nodeproto.ConcurrencySpec{Group: nA, Scope: new(scope)}, qA, qA)
			b := ConcurrencyKey(nodeproto.ConcurrencySpec{Group: nB, Scope: new(scope)}, qB, qB)
			if a == b {
				t.Fatalf("%s keys alias (%q,%q) and (%q,%q): %q", scope, qA, nA, qB, nB, a)
			}
		}
	})
}
