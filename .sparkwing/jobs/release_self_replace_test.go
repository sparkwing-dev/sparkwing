package jobs

import (
	"strings"
	"testing"
)

const fixtureBeforeCut = `module sparkwing-pipelines

go 1.26.0

require (
	github.com/sparkwing-dev/sparkwing v0.1.0
	golang.org/x/mod v0.35.0
)

require (
	github.com/aws/aws-sdk-go-v2 v1.41.7 // indirect
)

// SAFETY: Fixture pipelines resolve the local SDK checkout.
// The required version is used after removing this replacement.
replace github.com/sparkwing-dev/sparkwing => ..
`

func TestStripSelfReplace_BumpsRequireAndDropsReplace(t *testing.T) {
	output, changed, err := stripSelfReplace(fixtureBeforeCut, "v0.4.0")
	if err != nil {
		t.Fatalf("stripSelfReplace: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	if !strings.Contains(output, "github.com/sparkwing-dev/sparkwing v0.4.0") {
		t.Errorf("expected require bumped to v0.4.0; got:\n%s", output)
	}
	if strings.Contains(output, "v0.1.0") {
		t.Errorf("old require version v0.1.0 still present:\n%s", output)
	}
	if strings.Contains(output, "replace github.com/sparkwing-dev/sparkwing") {
		t.Errorf("replace line not stripped:\n%s", output)
	}
	if strings.Contains(output, "// SAFETY: Fixture pipelines resolve") {
		t.Errorf("self-replace comment block not stripped:\n%s", output)
	}
}

func TestStripSelfReplace_NoopWhenReplaceAbsent(t *testing.T) {
	body := `module sparkwing-pipelines

go 1.26.0

require github.com/sparkwing-dev/sparkwing v0.4.0
`
	output, changed, err := stripSelfReplace(body, "v0.4.0")
	if err != nil {
		t.Fatalf("stripSelfReplace: %v", err)
	}
	if changed {
		t.Errorf("expected changed=false when already pinned without a replacement; out:\n%s", output)
	}
	if output != body {
		t.Errorf("body modified unexpectedly:\nbytes(got)=%d bytes(want)=%d\n--- got %q ---\n--- want %q ---", len(output), len(body), output, body)
	}
}

func TestStripSelfReplace_BumpsRequireEvenWithoutReplace(t *testing.T) {
	body := `module sparkwing-pipelines

go 1.26.0

require github.com/sparkwing-dev/sparkwing v0.1.0
`
	output, changed, err := stripSelfReplace(body, "v0.4.0")
	if err != nil {
		t.Fatalf("stripSelfReplace: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true when require pin needs bumping")
	}
	if !strings.Contains(output, "v0.4.0") {
		t.Errorf("require not bumped:\n%s", output)
	}
}

func TestStripSelfReplace_ErrorOnMissingRequire(t *testing.T) {
	body := `module sparkwing-pipelines

go 1.26.0

require golang.org/x/mod v0.35.0
`
	_, _, err := stripSelfReplace(body, "v0.4.0")
	if err == nil {
		t.Fatal("expected error when sparkwing require is missing")
	}
}

func TestRestoreSelfReplace_AppendsWhenAbsent(t *testing.T) {
	body := `module sparkwing-pipelines

go 1.26.0

require github.com/sparkwing-dev/sparkwing v0.4.0
`
	output, changed := restoreSelfReplace(body)
	if !changed {
		t.Fatal("expected changed=true")
	}
	if !strings.Contains(output, "replace github.com/sparkwing-dev/sparkwing => ..") {
		t.Errorf("replace line not appended:\n%s", output)
	}
	if !strings.Contains(output, "// SAFETY: Pipeline jobs use the parent checkout to exercise SDK source changes.") {
		t.Errorf("comment block not appended:\n%s", output)
	}
	repeatedOutput, changedAgain := restoreSelfReplace(output)
	if changedAgain {
		t.Error("restoreSelfReplace not idempotent")
	}
	if repeatedOutput != output {
		t.Error("idempotent call modified body")
	}
}

func TestStripRestoreRoundTrip(t *testing.T) {
	stripped, changed, err := stripSelfReplace(fixtureBeforeCut, "v0.4.0")
	if err != nil || !changed {
		t.Fatalf("strip: changed=%v err=%v", changed, err)
	}
	restored, changed := restoreSelfReplace(stripped)
	if !changed {
		t.Fatal("restore: expected changed=true")
	}
	if !strings.Contains(restored, "github.com/sparkwing-dev/sparkwing v0.4.0") {
		t.Errorf("restored body missing bumped require:\n%s", restored)
	}
	if !strings.Contains(restored, "replace github.com/sparkwing-dev/sparkwing => ..") {
		t.Errorf("restored body missing replace:\n%s", restored)
	}
}

func TestGitTrackedPathsSplitsNULOutput(t *testing.T) {
	got := gitTrackedPaths("go.mod\x00internal/store/store.go\x00docs/file with spaces.md\x00")
	want := []string{"go.mod", "internal/store/store.go", "docs/file with spaces.md"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("gitTrackedPaths = %#v, want %#v", got, want)
	}
}
