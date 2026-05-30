package orchestrator

import (
	"context"
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// These tests pin ApplyProfileBackendsWithMirror's selection logic: it
// opens a local SQLite store into opts.MirrorLocal only when the profile
// resolves to a non-local state backend and mirroring is enabled. It no
// longer wraps opts.State (the tee moved to RunLocal at the StateBackend
// layer -- see mirror_state.go); opts.State stays the canonical handle.

func TestApplyProfileBackendsWithMirror_S3SetsMirrorLocal(t *testing.T) {
	neutralizeEnv(t)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	p := &profile.Profile{Name: "team", State: &backends.Spec{Type: backends.TypeS3, Bucket: "team", Prefix: "state"}}
	paths := Paths{Root: t.TempDir()}
	opts := Options{DefaultStateDB: paths.StateDB()}
	if err := ApplyProfileBackendsWithMirror(context.Background(), &opts, p, paths); err != nil {
		t.Fatalf("apply: %v", err)
	}
	defer opts.State.Close()
	if opts.MirrorLocal == nil {
		t.Fatal("MirrorLocal not opened for non-local s3 profile with MirrorLocal default")
	}
	defer opts.MirrorLocal.Close()
	if _, ok := opts.State.(*store.Store); ok {
		t.Fatal("s3 profile state should not be a local *store.Store")
	}
}

func TestApplyProfileBackendsWithMirror_S3NoMirrorWhenDisabled(t *testing.T) {
	neutralizeEnv(t)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	no := false
	p := &profile.Profile{
		Name:        "team",
		State:       &backends.Spec{Type: backends.TypeS3, Bucket: "team", Prefix: "state"},
		MirrorLocal: &no,
	}
	paths := Paths{Root: t.TempDir()}
	opts := Options{DefaultStateDB: paths.StateDB()}
	if err := ApplyProfileBackendsWithMirror(context.Background(), &opts, p, paths); err != nil {
		t.Fatalf("apply: %v", err)
	}
	defer opts.State.Close()
	if opts.MirrorLocal != nil {
		opts.MirrorLocal.Close()
		t.Fatal("MirrorLocal should stay nil when MirrorLocal=false")
	}
	if _, err := os.Stat(paths.StateDB()); !os.IsNotExist(err) {
		t.Errorf("no local sqlite should be created when mirror is disabled; stat err = %v", err)
	}
}

func TestApplyProfileBackendsWithMirror_ControllerSetsMirrorLocal(t *testing.T) {
	neutralizeEnv(t)
	p := &profile.Profile{Name: "prod", Controller: &profile.ControllerSpec{URL: "https://api.example.dev", Token: "swu_test"}}
	paths := Paths{Root: t.TempDir()}
	opts := Options{}
	if err := ApplyProfileBackendsWithMirror(context.Background(), &opts, p, paths); err != nil {
		t.Fatalf("apply(controller): %v", err)
	}
	defer opts.State.Close()
	if opts.MirrorLocal == nil {
		t.Fatal("MirrorLocal not opened for controller (non-local) profile")
	}
	opts.MirrorLocal.Close()
}

func TestApplyProfileBackendsWithMirror_LocalOnlyNoMirror(t *testing.T) {
	neutralizeEnv(t)
	p := &profile.Profile{Name: "prod", Controller: &profile.ControllerSpec{URL: "https://api.example.dev"}}
	paths := Paths{Root: t.TempDir()}
	opts := Options{LocalOnly: true, DefaultStateDB: paths.StateDB()}
	if err := ApplyProfileBackendsWithMirror(context.Background(), &opts, p, paths); err != nil {
		t.Fatalf("apply(LocalOnly): %v", err)
	}
	defer opts.State.Close()
	if opts.MirrorLocal != nil {
		opts.MirrorLocal.Close()
		t.Fatal("LocalOnly must never open a mirror")
	}
	if _, ok := opts.State.(*store.Store); !ok {
		t.Fatalf("LocalOnly State = %T, want *store.Store", opts.State)
	}
}
