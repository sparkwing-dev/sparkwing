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

// The pipelines tree is consumed as the same module path the SDK
// itself ships, so the require above is a placeholder; this replace
// pins it to the parent checkout (the sparkwing repo root). The
// pattern follows the standard "consumer .sparkwing/ uses a local
// replace during development" convention; here the parent IS the
// SDK rather than a sibling.
replace github.com/sparkwing-dev/sparkwing => ..
`

func TestStripSelfReplace_BumpsRequireAndDropsReplace(t *testing.T) {
	out, changed, err := stripSelfReplace(fixtureBeforeCut, "v0.4.0")
	if err != nil {
		t.Fatalf("stripSelfReplace: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	if !strings.Contains(out, "github.com/sparkwing-dev/sparkwing v0.4.0") {
		t.Errorf("expected require bumped to v0.4.0; got:\n%s", out)
	}
	if strings.Contains(out, "v0.1.0") {
		t.Errorf("old require version v0.1.0 still present:\n%s", out)
	}
	if strings.Contains(out, "replace github.com/sparkwing-dev/sparkwing") {
		t.Errorf("replace line not stripped:\n%s", out)
	}
	if strings.Contains(out, "// The pipelines tree is consumed") {
		t.Errorf("self-replace comment block not stripped:\n%s", out)
	}
}

func TestStripSelfReplace_NoopWhenReplaceAbsent(t *testing.T) {
	body := `module sparkwing-pipelines

go 1.26.0

require github.com/sparkwing-dev/sparkwing v0.4.0
`
	out, changed, err := stripSelfReplace(body, "v0.4.0")
	if err != nil {
		t.Fatalf("stripSelfReplace: %v", err)
	}
	if changed {
		t.Errorf("expected changed=false when already in shipped shape; out:\n%s", out)
	}
	if out != body {
		t.Errorf("body modified unexpectedly:\nbytes(got)=%d bytes(want)=%d\n--- got %q ---\n--- want %q ---", len(out), len(body), out, body)
	}
}

func TestStripSelfReplace_BumpsRequireEvenWithoutReplace(t *testing.T) {
	body := `module sparkwing-pipelines

go 1.26.0

require github.com/sparkwing-dev/sparkwing v0.1.0
`
	out, changed, err := stripSelfReplace(body, "v0.4.0")
	if err != nil {
		t.Fatalf("stripSelfReplace: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true when require pin needs bumping")
	}
	if !strings.Contains(out, "v0.4.0") {
		t.Errorf("require not bumped:\n%s", out)
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
	out, changed := restoreSelfReplace(body)
	if !changed {
		t.Fatal("expected changed=true")
	}
	if !strings.Contains(out, "replace github.com/sparkwing-dev/sparkwing => ..") {
		t.Errorf("replace line not appended:\n%s", out)
	}
	if !strings.Contains(out, "// The pipelines tree is consumed") {
		t.Errorf("comment block not appended:\n%s", out)
	}
	out2, changed2 := restoreSelfReplace(out)
	if changed2 {
		t.Error("restoreSelfReplace not idempotent")
	}
	if out2 != out {
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
