package installsite_test

import (
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/installsite"
)

func TestPathKeyIsStablePerInstall(t *testing.T) {
	a := filepath.Join("/opt", "a", installsite.ExeName())
	b := filepath.Join("/opt", "b", installsite.ExeName())
	if again := filepath.Clean(a); installsite.PathKey(a) != installsite.PathKey(again) {
		t.Fatalf("one install got two keys: %q", a)
	}
	if installsite.PathKey(a) == installsite.PathKey(b) {
		t.Fatalf("two distinct installs share a key: %q vs %q", a, b)
	}
}

func TestSelfIsAnAbsoluteResolvedPath(t *testing.T) {
	self, err := installsite.Self()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(self) {
		t.Fatalf("Self returned a relative path: %q", self)
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil && resolved != self {
		t.Fatalf("Self returned %q, which still resolves to %q", self, resolved)
	}
}
