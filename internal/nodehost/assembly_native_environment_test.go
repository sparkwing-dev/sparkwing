package nodehost

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func TestRemotePreparationKeepsTheHostToolEnvironmentWithoutLocalSecrets(t *testing.T) {
	cfg, state := nativeConfigFixture(t)
	const secret = "http://deploy:local-only-password@proxy.internal:3128"
	cfg.Source.Base = procenv.Base{Ambient: []string{"HOME=/home/dev", "PATH=/usr/local/go/bin:/usr/bin", "GOCACHE=/home/dev/.cache/go-build", "HTTPS_PROXY=" + secret}, SubmitEnvAllow: []string{"GOCACHE", "GOMODCACHE"}}
	state.snapshot.Run.Request.Request.LocalSecretNames = []string{"HTTPS_PROXY"}
	state.snapshot.Run.Request.Request.Env.Names = nil
	services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) {
		return &processListener{closed: make(chan struct{})}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := services.Close(); err != nil {
			t.Error(err)
		}
	})
	base := services.Config.Source.Base
	prepared, err := procenv.Build(procenv.Install, base, procenv.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	install, err := procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: slices.Collect(prepared.Values()), Masker: base.Masker}, procenv.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"HOME=/home/dev", "PATH=/usr/local/go/bin:/usr/bin", "GOCACHE=/home/dev/.cache/go-build"} {
		if !slices.Contains(slices.Collect(install.Values()), want) {
			t.Errorf("SDK install environment lacks %s: %v", want, install)
		}
	}
	if strings.Contains(strings.Join(slices.Collect(install.Values()), "\n"), "local-only-password") {
		t.Fatalf("SDK install environment carries a configured local secret: %v", install)
	}
	if !base.Masker.ContainsSecret(secret) {
		t.Fatal("withheld local secret is not masked")
	}
}

func TestNativeHostingOwnsAndCleansTemporaryStorage(t *testing.T) {
	cfg, _ := nativeConfigFixture(t)
	services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) {
		return &processListener{closed: make(chan struct{})}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := services.Close(); err != nil {
			t.Error(err)
		}
	})
	base := services.Config.Source.Base
	var temporary string
	for _, kind := range []procenv.Kind{procenv.Install, procenv.Git, procenv.Plan, procenv.Attempt, procenv.Eval} {
		environment, err := procenv.Build(kind, base, procenv.Runtime{})
		if err != nil {
			t.Fatal(err)
		}
		directory := environment.Lookup("TMPDIR")
		if directory == "" || filepath.Dir(directory) != filepath.Join(cfg.Home, "tmp") {
			t.Fatalf("kind %v temp directory = %q", kind, directory)
		}
		if temporary != "" && temporary != directory {
			t.Fatal("hosting changed temp directories")
		}
		temporary = directory
		if info, err := os.Stat(directory); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Fatalf("private temp storage: %v %v", info, err)
		}
	}
	if err := services.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("temp storage survived close: %v", err)
	}
}
