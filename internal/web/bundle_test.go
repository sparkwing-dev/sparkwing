package web

import (
	"strings"
	"testing"
	"testing/fstest"
)

// The reader of a skip reason is someone whose suite just went quiet, so the
// reason has to name the command that builds the bundle rather than the
// symptom.
func TestBundleSkipReasonNamesTheBuildStepWhenOnlyTheGitkeepIsEmbedded(t *testing.T) {
	reason := bundleSkipReason(fstest.MapFS{"next-out/.gitkeep": &fstest.MapFile{}})
	if reason == "" {
		t.Fatal("a bundle holding only .gitkeep must stop a test that needs a served dashboard")
	}
	if !strings.Contains(reason, "bin/build-web.sh") {
		t.Errorf("skip reason does not name the command that builds the bundle: %q", reason)
	}
}

// A checkout that has built the dashboard must run these tests, not skip them.
func TestBundleSkipReasonIsEmptyWhenTheBundleIsBuilt(t *testing.T) {
	built := fstest.MapFS{"next-out/index.html": &fstest.MapFile{Data: []byte("<title>Sparkwing</title>")}}
	if reason := bundleSkipReason(built); reason != "" {
		t.Errorf("a built bundle must run the dashboard tests, not skip them: %q", reason)
	}
}

// The startup guard and the skip guard answer the same question, so they
// cannot be allowed to drift into two answers.
func TestVerifyBundleEmbeddedAndBundleSkipReasonReadTheSameBundle(t *testing.T) {
	if (VerifyBundleEmbedded() == nil) != (BundleSkipReason() == "") {
		t.Fatalf("the startup guard and the skip guard disagree about this binary's bundle: "+
			"VerifyBundleEmbedded=%v BundleSkipReason=%q", VerifyBundleEmbedded(), BundleSkipReason())
	}
}
