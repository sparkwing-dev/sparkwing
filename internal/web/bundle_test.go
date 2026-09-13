package web

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestBundleMissingReasonNamesTheBuildStepWhenOnlyTheGitkeepIsEmbedded(t *testing.T) {
	reason := bundleMissingReason(fstest.MapFS{"next-out/.gitkeep": &fstest.MapFile{}})
	if reason == "" {
		t.Fatal("a bundle holding only .gitkeep must not read as a built dashboard")
	}
	if !strings.Contains(reason, "bin/build-web.sh") {
		t.Errorf("skip reason does not name the command that builds the bundle: %q", reason)
	}
}

func TestBundleMissingReasonIsEmptyWhenTheBundleIsBuilt(t *testing.T) {
	built := fstest.MapFS{"next-out/index.html": &fstest.MapFile{Data: []byte("<title>Sparkwing</title>")}}
	if reason := bundleMissingReason(built); reason != "" {
		t.Errorf("a built bundle must read as built: %q", reason)
	}
}

func TestVerifyBundleRefusesASuppliedBundleWithNoIndex(t *testing.T) {
	if err := VerifyBundle(fstest.MapFS{"_next/static/app.js": &fstest.MapFile{}}); err == nil {
		t.Fatal("a supplied bundle with no index.html must not be served as a dashboard")
	}
	if err := VerifyBundle(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<title>Sparkwing</title>")}}); err != nil {
		t.Errorf("a supplied bundle rooted at index.html must be served: %v", err)
	}
}
