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
