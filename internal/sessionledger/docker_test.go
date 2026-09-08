package sessionledger

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func dockerReachable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not reachable")
	}
}

func TestDockerRecordsFromOneOwnerDoNotCollide(t *testing.T) {
	l := OpenWithProbe(filepath.Join(t.TempDir(), "sessions"), &fakeProbe{})
	mk := func(name string) {
		if _, err := l.Record(Record{
			Run: "run-1", Node: "build", OwnerPID: 4242, OwnerBirth: "b",
			Handle: Handle{Kind: "docker", Container: name}, Command: "docker run x",
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk("sparkwing-run-aaaa")
	mk("sparkwing-run-bbbb")
	got, err := l.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("two containers from one owner collapsed to %d record(s)", len(got))
	}
}

func TestDockerRecordRoundTrips(t *testing.T) {
	l := OpenWithProbe(filepath.Join(t.TempDir(), "sessions"), &fakeProbe{})
	if _, err := l.Record(Record{
		Run: "run-1", Node: "build", OwnerPID: 4242, OwnerBirth: "b",
		Handle: Handle{Kind: "docker", Container: "sparkwing-run-cafe"}, Command: "docker run node:22",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := l.List()
	if len(got) != 1 || got[0].Handle.Kind != "docker" || got[0].Handle.Container != "sparkwing-run-cafe" {
		t.Fatalf("docker record did not round-trip: %+v", got)
	}
}

func TestTerminateContainerRefusesAnEmptyName(t *testing.T) {
	// safety: this exercises the docker routing in dispatchProbe without a
	// daemon: the empty-name guard returns before docker is ever invoked.
	err := dispatchProbe{platform: &fakeProbe{}}.Terminate(context.Background(), Handle{Kind: "docker"})
	if err == nil {
		t.Fatal("dispatch accepted a docker handle with no container name")
	}
}

func TestDispatchDelegatesNonDockerToThePlatformProbe(t *testing.T) {
	fp := &fakeProbe{}
	if err := (dispatchProbe{platform: fp}).Terminate(context.Background(), Handle{Kind: "session", LeaderPID: 5, SessionID: 5}); err != nil {
		t.Fatal(err)
	}
	if len(fp.terminated) != 1 || fp.terminated[0].Kind != "session" {
		t.Fatalf("session handle was not delegated to the platform probe: %+v", fp.terminated)
	}
}

func TestRemoveContainersForRun_RealDocker(t *testing.T) {
	dockerReachable(t)
	ctx := context.Background()
	run := "sw-test-run-" + randomLedgerSuffix()
	name := "sparkwing-test-" + randomLedgerSuffix()
	if out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--label", "sparkwing.run="+run,
		"--name", name, "busybox", "sleep", "300").CombinedOutput(); err != nil {
		t.Skipf("cannot start test container: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	ids, err := ContainersForRun(ctx, run)
	if err != nil || len(ids) != 1 {
		t.Fatalf("ContainersForRun = %v, %v, want one id", ids, err)
	}
	removed, err := RemoveContainersForRun(ctx, run)
	if err != nil || len(removed) != 1 {
		t.Fatalf("RemoveContainersForRun = %v, %v, want one removed", removed, err)
	}
	after, _ := ContainersForRun(ctx, run)
	if len(after) != 0 {
		t.Fatalf("container survived removal: %v", after)
	}
	// idempotent: removing again is not an error
	if _, err := RemoveContainersForRun(ctx, run); err != nil {
		t.Fatalf("second removal errored: %v", err)
	}
}
