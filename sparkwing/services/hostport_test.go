package services

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishedPort(t *testing.T) {
	for _, tc := range []struct {
		name string
		svc  Service
		want int
	}{
		{"a declared port publishes itself", Service{Port: 5432}, 5432},
		{"a pinned host port wins over the container port", Service{Port: 5432, HostPort: 15432}, 15432},
		{"host networking publishes nothing", Service{}, 0},
		{"host networking ignores a pinned host port", Service{HostPort: 15432}, 0},
	} {
		if got := publishedPort(tc.svc); got != tc.want {
			t.Errorf("%s: publishedPort = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestResolveHostPortsHoldsEveryProbeUntilAllAreChosen(t *testing.T) {
	services := []Service{
		{Name: "first", Port: 5432, HostPort: AutoPort},
		{Name: "second", Port: 6379, HostPort: AutoPort},
		{Name: "pinned", Port: 80, HostPort: 18080},
		{Name: "hostnet", Port: 0},
	}
	release, err := resolveHostPorts(services)
	if err != nil {
		t.Fatalf("resolveHostPorts: %v", err)
	}

	if services[2].HostPort != 18080 {
		t.Errorf("a pinned host port was reassigned to %d", services[2].HostPort)
	}
	if services[3].HostPort != 0 {
		t.Errorf("a host-networking service was given host port %d", services[3].HostPort)
	}
	for _, i := range []int{0, 1} {
		if services[i].HostPort <= 0 {
			t.Fatalf("%s was left at %d, want a port the OS reported free", services[i].Name, services[i].HostPort)
		}
	}
	if services[0].HostPort == services[1].HostPort {
		t.Fatalf("both services were handed port %d; the probes were not held", services[0].HostPort)
	}

	for _, i := range []int{0, 1} {
		probe, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", services[i].HostPort))
		if err == nil {
			_ = probe.Close()
			release()
			t.Fatalf("port %d was bindable before release; it was not reserved", services[i].HostPort)
		}
	}

	release()

	for _, i := range []int{0, 1} {
		probe, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", services[i].HostPort))
		if err != nil {
			t.Fatalf("port %d was still held after release: %v", services[i].HostPort, err)
		}
		_ = probe.Close()
	}
}

func TestResolveHostPortsRefusesAutoPortWithNoContainerPort(t *testing.T) {
	services := []Service{{Name: "no-container-port", HostPort: AutoPort}}
	release, err := resolveHostPorts(services)
	if err == nil {
		release()
		t.Fatal("resolveHostPorts accepted AutoPort with no container port")
	}
	if !strings.Contains(err.Error(), "no-container-port") {
		t.Errorf("error does not name the service: %v", err)
	}
}

func stubDocker(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(dir, "docker")); err != nil {
		t.Fatal(err)
	}
	t.Setenv(stubDockerArgvEnv, argv)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argv
}

const stubDockerArgvEnv = "SERVICES_TEST_DOCKER_ARGV"

// hack: the stub binds every published host port the way docker does, so a port
// this package is still holding fails the test rather than a user.
func runStubDocker(args []string) int {
	if path := os.Getenv(stubDockerArgvEnv); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintln(f, strings.Join(args, " "))
			_ = f.Close()
		}
	}
	for i, arg := range args {
		if arg != "-p" || i+1 >= len(args) {
			continue
		}
		fields := strings.Split(args[i+1], ":")
		if len(fields) != 3 {
			continue
		}
		listener, err := net.Listen("tcp", fields[0]+":"+fields[1])
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "docker: bind %s: %v\n", args[i+1], err)
			return 125
		}
		_ = listener.Close()
	}
	return 0
}

func TestWithServicesAddrsPublishesTheAllocatedPort(t *testing.T) {
	argvPath := stubDocker(t)

	var got []Addr
	err := WithServicesAddrs(grantedCtx(context.Background()), []Service{
		{Name: "db", Image: "postgres:15", Port: 5432, HostPort: AutoPort},
	}, func(_ context.Context, addrs []Addr) error {
		got = addrs
		return nil
	})
	if err != nil {
		t.Fatalf("WithServicesAddrs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d addrs, want 1", len(got))
	}
	if got[0].Name != "db" {
		t.Errorf("addr name = %q, want db", got[0].Name)
	}
	if got[0].HostPort <= 0 {
		t.Fatalf("addr host port = %d, want the allocated port", got[0].HostPort)
	}

	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("read stub argv: %v", err)
	}
	want := fmt.Sprintf("-p 127.0.0.1:%d:5432", got[0].HostPort)
	if !strings.Contains(string(argv), want) {
		t.Errorf("docker was run with:\n%s\nwant a publish of %q", argv, want)
	}
	if strings.Contains(string(argv), fmt.Sprintf("127.0.0.1:%d:%d", got[0].HostPort, got[0].HostPort)) {
		t.Errorf("the allocated port was published on both sides of the mapping:\n%s", argv)
	}
}

func TestWithServicesAddrsReportsAPinnedPortAndHostNetworking(t *testing.T) {
	stubDocker(t)

	var got []Addr
	err := WithServicesAddrs(grantedCtx(context.Background()), []Service{
		{Name: "pinned", Image: "redis:7", Port: 6379, HostPort: 16379},
		{Name: "hostnet", Image: "busybox"},
	}, func(_ context.Context, addrs []Addr) error {
		got = addrs
		return nil
	})
	if err != nil {
		t.Fatalf("WithServicesAddrs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d addrs, want 2", len(got))
	}
	if got[0].HostPort != 16379 {
		t.Errorf("pinned addr = %d, want 16379", got[0].HostPort)
	}
	if got[1].HostPort != 0 {
		t.Errorf("host-networking addr = %d, want 0", got[1].HostPort)
	}
}

func TestWithServicesStillRunsFnWithoutAddrs(t *testing.T) {
	stubDocker(t)

	called := false
	err := WithServices(grantedCtx(context.Background()), []Service{
		{Name: "db", Image: "postgres:15", Port: 5432, HostPort: AutoPort},
	}, func(context.Context) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("WithServices: %v", err)
	}
	if !called {
		t.Fatal("fn was not called")
	}
}
