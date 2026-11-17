package depcachekey

import (
	"testing"
)

func TestPublicBytesSeparatesInputsAndScope(t *testing.T) {
	a, err := Bytes("tools", "scope", []byte("lock"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := Bytes("tools", "scope", []byte("lock"))
	if err != nil || a != again {
		t.Fatalf("unstable key %q %q: %v", a, again, err)
	}
	for _, tc := range []struct{ name, scope, data string }{{"other", "scope", "lock"}, {"tools", "other", "lock"}, {"tools", "scope", "other"}} {
		b, err := Bytes(tc.name, tc.scope, []byte(tc.data))
		if err != nil || a == b {
			t.Fatalf("collision for %+v: %v", tc, err)
		}
	}
}
