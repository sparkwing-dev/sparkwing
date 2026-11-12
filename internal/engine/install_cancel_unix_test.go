//go:build !windows

package engine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestRunningSDKCancellationDoesNotReportCleanupFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { cancel(); <-r.Context().Done() }))
	defer server.Close()
	key := testSigningKey(t)
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	version := "v0.20.0"
	fixture := `package main
import("net/http";"os")
var Version="dev"
func main(){ if _,err:=http.Get(os.Getenv("INSTALL_CANCEL_URL"));err!=nil{os.Exit(31)} }
`
	bundle := signedBundle(t, key, version, target, fixtureExecutableSource(t, version, target, fixture))
	release, err := VerifyRelease(version, target, bundle, signingKeys(key))
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(Config{TrustedKeys: signingKeys(key), CacheDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	checkout := t.TempDir()
	pipeline := filepath.Join(checkout, ".sparkwing")
	if err := os.Mkdir(pipeline, 0o700); err != nil {
		t.Fatal(err)
	}
	writeInstallPin(t, pipeline, engineid.GoSDK)
	pin, err := ReadPin(pipeline, engineid.GoSDK)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.storeVerified(t.Context(), pin, release)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolved.InstallSDK(ctx, SDKInstallRequest{Directory: checkout, PipelineDir: ".sparkwing", SDK: engineid.GoSDK, Environment: []string{"INSTALL_CANCEL_URL=" + server.URL}})
	var exit *exec.ExitError
	if !errors.Is(err, context.Canceled) || errors.Is(err, procgroup.ErrCleanup) || errors.As(err, &exit) {
		t.Fatalf("a reaped cancelled installer reported a real failure: %v", err)
	}
}

func TestIntegrityDiagnosticPreservesCancellation(t *testing.T) {
	for name, convert := range map[string]func(error) error{
		"release": integrity,
		"cache":   func(err error) error { return cachedIntegrity("/fixture/engine", err) },
	} {
		t.Run(name, func(t *testing.T) {
			genuine := errors.New("verification read failed")
			for _, cancellation := range []error{context.Canceled, context.DeadlineExceeded} {
				err := convert(cancellation)
				if !errors.Is(err, cancellation) {
					t.Errorf("cancellation became a false integrity failure: %v", err)
				}
				err = convert(errors.Join(cancellation, genuine))
				if !errors.Is(err, cancellation) || !errors.Is(err, genuine) {
					t.Errorf("joined real failure was lost: %v", err)
				}
			}
			var failure *apierr.Error
			if err := convert(genuine); !errors.As(err, &failure) || failure.Code != hosterr.IntegrityFailure || failure.Hint == "" {
				t.Fatalf("real integrity failure lost its code or repair: %v", err)
			}
		})
	}
}
