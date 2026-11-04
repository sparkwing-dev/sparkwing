package web

import (
	"io/fs"
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
		t.Errorf("the reason does not name the command that builds the bundle: %q", reason)
	}
}

func TestBundleMissingReasonIsEmptyWhenTheBundleIsBuilt(t *testing.T) {
	built := fstest.MapFS{"next-out/index.html": &fstest.MapFile{Data: []byte("<title>Sparkwing</title>")}}
	if reason := bundleMissingReason(built); reason != "" {
		t.Errorf("a built bundle must read as built: %q", reason)
	}
}

func TestVerifyBundleRefusesASuppliedBundleWithNoIndex(t *testing.T) {
	if err := verifyBundle(fstest.MapFS{"_next/static/app.js": &fstest.MapFile{}}); err == nil {
		t.Fatal("a supplied bundle with no index.html must not be served as a dashboard")
	}
	if err := verifyBundle(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<title>Sparkwing</title>")}}); err != nil {
		t.Errorf("a supplied bundle rooted at index.html must be served: %v", err)
	}
}

func TestVerifiedBundleIsNilWithoutAnIndex(t *testing.T) {
	if verifiedBundle(fstest.MapFS{"next-out/.keep": {}}) != nil {
		t.Fatal("a bundle without index.html was served, which answers 404 on every page")
	}
	bundle := verifiedBundle(fstest.MapFS{"next-out/index.html": {Data: []byte("<html>")}})
	if bundle == nil {
		t.Fatal("a built bundle was refused")
	}
	if _, err := fs.Stat(bundle, "index.html"); err != nil {
		t.Fatalf("bundle is not rooted at index.html: %v", err)
	}
}
