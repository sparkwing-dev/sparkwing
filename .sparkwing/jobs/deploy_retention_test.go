package jobs

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestDeployRetentionTagsIndexAndEveryChild(t *testing.T) {
	in, _ := publishFixture(t, "amd64", "arm64")
	ref, err := name.NewDigest(in.ControllerBase)
	if err != nil {
		t.Fatal(err)
	}
	index, err := remote.Index(ref)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := index.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := retainDeployManifest(ref); err != nil {
			t.Fatal(err)
		}
	}
	digests := []string{ref.DigestStr()}
	for _, child := range manifest.Manifests {
		digests = append(digests, child.Digest.String())
	}
	for _, digest := range digests {
		tag := ref.Context().Tag("cloud-deploy-" + strings.TrimPrefix(digest, "sha256:"))
		got, err := remote.Head(tag)
		if err != nil {
			t.Fatal(err)
		}
		if got.Digest.String() != digest {
			t.Fatalf("%s = %s, want %s", tag, got.Digest, digest)
		}
	}
}

func TestDeployRetainsCandidateAndRollbackBeforePinning(t *testing.T) {
	f := newFakeDeploy(t)
	images := f.candidate()
	if _, _, err := f.deployer(false).deployTo(t.Context(), f.config().Staging, images); err != nil {
		t.Fatal(err)
	}
	for _, want := range []imagePin{images.Controller, images.Runner, f.old.Controller, f.old.Runner} {
		found := false
		for _, got := range f.registry.retained {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("did not retain %s", want.ref())
		}
	}
}

func TestDeployRetentionFailureLeavesGitopsUnchanged(t *testing.T) {
	f := newFakeDeploy(t)
	before := deployGitIn(t, f.bare, "rev-parse", "main")
	f.registry.retainErr = errors.New("retention denied")
	if _, _, err := f.deployer(false).deployTo(t.Context(), f.config().Staging, f.candidate()); err == nil || !strings.Contains(err.Error(), "retention denied") {
		t.Fatalf("got %v", err)
	}
	if after := deployGitIn(t, f.bare, "rev-parse", "main"); after != before {
		t.Fatal("changed GitOps before retention succeeded")
	}
}

func TestDeployRetentionRejectsConflictingTag(t *testing.T) {
	in, _ := publishFixture(t, "amd64", "arm64")
	ref, err := name.NewDigest(in.ControllerBase)
	if err != nil {
		t.Fatal(err)
	}
	other, err := name.NewDigest(in.RunnerBase)
	if err != nil {
		t.Fatal(err)
	}
	index, err := remote.Index(other)
	if err != nil {
		t.Fatal(err)
	}
	tag := ref.Context().Tag("cloud-deploy-" + strings.TrimPrefix(ref.DigestStr(), "sha256:"))
	if err := remote.WriteIndex(tag, index); err != nil {
		t.Fatal(err)
	}
	if err := retainDeployManifest(ref); err == nil || !strings.Contains(err.Error(), "expected") {
		t.Fatalf("conflicting retention tag: %v", err)
	}
}
