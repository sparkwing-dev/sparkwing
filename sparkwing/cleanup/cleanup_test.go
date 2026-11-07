package cleanup

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
)

type cleanupFixture struct {
	sdkserve.Services
	argv        []string
	description string
	released    bool
	releaseErr  error
}

func (f *cleanupFixture) RegisterCleanup(_ context.Context, argv []string, description string) (string, error) {
	f.argv = argv
	f.description = description
	return "owned", nil
}
func (f *cleanupFixture) ReleaseCleanup(ctx context.Context, id string) error {
	f.released = id == "owned"
	f.releaseErr = ctx.Err()
	return nil
}
func TestCleanupRelaysToHostAndReleasesAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	host := &cleanupFixture{}
	ctx = sdkserve.WithServices(ctx, host)
	release, err := Register(ctx, Spec{Argv: []string{"docker", "rm", "-f", "owned"}, Description: "container"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	release()
	if len(host.argv) != 4 || host.description != "container" || !host.released || host.releaseErr != nil {
		t.Fatalf("host=%+v", host)
	}
}
func TestCleanupRequiresHost(t *testing.T) {
	if _, err := Register(t.Context(), Spec{Argv: []string{"cleanup"}}); err == nil {
		t.Fatal("cleanup registered without a host")
	}
}
func TestEmptyCleanupNeedsNoHost(t *testing.T) {
	release, err := Register(t.Context(), Spec{})
	if err != nil {
		t.Fatal(err)
	}
	release()
}
