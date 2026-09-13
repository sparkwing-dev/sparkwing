package web

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestBundleReasonNamesTheBuildStepWhenOnlyTheGitkeepIsEmbedded(t *testing.T) {
	reason := bundleSkipReason(fstest.MapFS{"next-out/.gitkeep": &fstest.MapFile{}})
	if reason == "" {
		t.Fatal("a bundle holding only .gitkeep must not read as a built dashboard")
	}
	if !strings.Contains(reason, "bin/build-web.sh") {
		t.Errorf("skip reason does not name the command that builds the bundle: %q", reason)
	}
}

func TestBundleReasonIsEmptyWhenTheBundleIsBuilt(t *testing.T) {
	built := fstest.MapFS{"next-out/index.html": &fstest.MapFile{Data: []byte("<title>Sparkwing</title>")}}
	if reason := bundleSkipReason(built); reason != "" {
		t.Errorf("a built bundle must read as built: %q", reason)
	}
}

func TestVerifyBundleEmbeddedAcceptsABuiltBundleAndRefusesAnEmptyOne(t *testing.T) {
	if (VerifyBundleEmbedded() == nil) != (bundleSkipReason(nextBundle) == "") {
		t.Fatalf("the startup guard and the bundle reader disagree about this binary's bundle: "+
			"VerifyBundleEmbedded=%v reason=%q", VerifyBundleEmbedded(), bundleSkipReason(nextBundle))
	}
}
