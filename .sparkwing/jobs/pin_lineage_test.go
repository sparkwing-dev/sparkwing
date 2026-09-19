package jobs

import (
	"context"
	"path/filepath"
	"testing"
)

func TestLatestReleasedTagIgnoresATagOffThisLine(t *testing.T) {
	repo := seedReleaseRepo(t)
	gitRun(t, repo, "tag", "v0.2.0")

	gitRun(t, repo, "checkout", "-q", "-b", "side")
	writeFile(t, filepath.Join(repo, "doc.go"), "package sparkwing\n\n// a side branch\n")
	gitRun(t, repo, "add", "doc.go")
	gitRun(t, repo, "commit", "-m", "a release cut somewhere that never landed")
	gitRun(t, repo, "tag", "v9.9.9")
	gitRun(t, repo, "checkout", "-q", "main")

	latest, err := latestReleasedTag(grantedCtx(context.Background()), repo, -1)
	if err != nil {
		t.Fatal(err)
	}
	if latest != "v0.2.0" {
		t.Fatalf("latest release on this line = %q, want v0.2.0; a tag the line never merged is not its latest release", latest)
	}
}

func TestLatestReleasedTagStillReadsATagThisLineCarries(t *testing.T) {
	repo := seedReleaseRepo(t)
	gitRun(t, repo, "tag", "v0.2.0")
	writeFile(t, filepath.Join(repo, "doc.go"), "package sparkwing\n\n// later\n")
	gitRun(t, repo, "add", "doc.go")
	gitRun(t, repo, "commit", "-m", "later")
	gitRun(t, repo, "tag", "v0.3.0")

	latest, err := latestReleasedTag(grantedCtx(context.Background()), repo, -1)
	if err != nil {
		t.Fatal(err)
	}
	if latest != "v0.3.0" {
		t.Fatalf("latest release on this line = %q, want v0.3.0", latest)
	}
}
