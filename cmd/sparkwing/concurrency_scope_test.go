package main

import "testing"

// Defect 10: a global group whose name contains the qualifier separator
// must still be labeled "global" -- the label reads the scheme tag, not
// the mere presence of an "@".
func TestScopeFromKey_GlobalNameWithAtNotMislabeled(t *testing.T) {
	if got := scopeFromKey("g:payments@db"); got != "global" {
		t.Fatalf("scopeFromKey(global key with @ in name) = %q, want global", got)
	}
	if got := scopeFromKey("b:5:host1db"); got != "box (host1)" {
		t.Fatalf("scopeFromKey(box key) = %q, want box (host1)", got)
	}
	if got := scopeFromKey("memo:abc123"); got != "content-cache" {
		t.Fatalf("scopeFromKey(memo key) = %q, want content-cache", got)
	}
}
