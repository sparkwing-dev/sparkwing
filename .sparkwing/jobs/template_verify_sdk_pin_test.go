package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPinLocalSparkwingSDK_AppendsTheTreeReplace pins that a scaffold's
// go.mod gains a filesystem replace onto the working tree, because a
// scaffold left on the released SDK is not verifying the release being
// cut -- and across a schema bump it cannot open the shared verify
// state home at all.
func TestPinLocalSparkwingSDK_AppendsTheTreeReplace(t *testing.T) {
	dir := t.TempDir()
	// A go.mod that cannot resolve from the proxy proves the rewrite is
	// what go mod tidy consumed: tidy against a fake module succeeds only
	// because the replace points somewhere real.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/verify/pipelines\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := pinLocalSparkwingSDK(context.Background(), dir, root); err != nil {
		t.Fatalf("pinLocalSparkwingSDK: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	want := "replace github.com/sparkwing-dev/sparkwing => " + root
	if !strings.Contains(string(body), want) {
		t.Fatalf("go.mod missing %q:\n%s", want, body)
	}
}

// TestPinLocalSparkwingSDK_NoRootIsANoOp pins the empty-root guard so a
// caller without a resolved tree cannot write a replace onto "".
func TestPinLocalSparkwingSDK_NoRootIsANoOp(t *testing.T) {
	dir := t.TempDir()
	orig := "module example.com/verify/pipelines\n\ngo 1.24\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pinLocalSparkwingSDK(context.Background(), dir, ""); err != nil {
		t.Fatalf("pinLocalSparkwingSDK: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	if string(body) != orig {
		t.Fatalf("go.mod changed on empty root:\n%s", body)
	}
}
