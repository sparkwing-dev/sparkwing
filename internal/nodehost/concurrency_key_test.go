package nodehost

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestParseConcurrencyKeyRefusesMalformedTuples(t *testing.T) {
	for _, key := range []string{"", "db", "other:db", "job:run:id:db", "job:run:-1:id:db", "job:box:+1:a:db", "job:run:01:a:db", "job:run:1:", "job:box:2:a:db", "job:run:999999999999999999999999:x:db"} {
		if _, _, _, _, ok := runspec.ParseConcurrencyKey(key); ok {
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
			key := runspec.ConcurrencyKey("job", scope, identity, group)
			gotLevel, gotScope, gotIdentity, gotGroup, ok := runspec.ParseConcurrencyKey(key)
			wantIdentity := identity
			if scope == "global" {
				wantIdentity = ""
			}
			if !ok || gotLevel != "job" || gotScope != scope || gotIdentity != wantIdentity || gotGroup != group || keys[key] {
				t.Fatalf("tuple (%q,%q,%q) encoded as %q and decoded as (%q,%q,%q,%t)", scope, wantIdentity, group, key, gotScope, gotIdentity, gotGroup, ok)
			}
			keys[key] = true
		}
	})
}

func TestScopeKeySeparatesScopes(t *testing.T) {
	global := runspec.ConcurrencyKey("job", "global", "", "collide@advbox-host")
	box := runspec.ConcurrencyKey("job", "box", "advbox-host", "collide")
	if global == box {
		t.Fatalf("global and box keys collide: %q", global)
	}
	for _, tc := range []struct{ scope, name, want string }{
		{"global", "payments@db", "global"},
		{"box", "db", "box (host1)"},
		{"run", "db", "run (run-7)"},
	} {
		key := fixtureJobCapacityKey(tc.name, tc.scope, "run-7", "host1")
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
		key := fixtureJobCapacityKey("db", tc.scope, "run:1@x", "host:with@seps")
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
			a := runspec.ConcurrencyKey("job", scope, qA, nA)
			b := runspec.ConcurrencyKey("job", scope, qB, nB)
			if a == b {
				t.Fatalf("%s keys alias (%q,%q) and (%q,%q): %q", scope, qA, nA, qB, nB, a)
			}
		}
	})
}

func fixtureJobCapacityKey(name, scope, runID, boxID string) string {
	identity := ""
	switch scope {
	case "run":
		identity = runID
	case "box":
		identity = boxID
	default:
		scope = "global"
	}
	return runspec.ConcurrencyKey("job", scope, identity, name)
}
