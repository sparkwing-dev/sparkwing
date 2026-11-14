package client

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierrhttp"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestStopReturnsTheControllersBackgroundStopUnchanged(t *testing.T) {
	home := shortHome(t)
	sent := &apierr.Error{Code: "unavailable", Message: "local runner R (pid 7) is still finishing held work; the local controller stops on its own once that runner exits", Hint: "Run `sparkwing daemon status` to check."}
	serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != controllerPath+"drain" || r.URL.Query().Get("runner") != "retire" {
			http.NotFound(w, r)
			return
		}
		_ = apierrhttp.Write(w, sent)
	}))
	err := Stop(t.Context(), Options{Home: home, RetireRunner: true})
	var failure *apierr.Error
	if !errors.As(err, &failure) || err.Error() != sent.Message || failure.Hint != sent.Hint {
		t.Fatalf("stop = %v (%#v); want the controller's explanation unchanged", err, failure)
	}
}

func TestEnsureDaemonNamesAControllerStillStopping(t *testing.T) {
	home := shortHome(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var probes atomic.Int32
	serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != controllerPath+"probe" {
			http.NotFound(w, r)
			return
		}
		if probes.Add(1) == 3 {
			cancel()
		}
		writeHTTPFixture(w, wingwire.HelloAck{ProtocolMajor: wingd.ProtocolMajor, NativeProtocolMajor: wingd.ProtocolMajor, BinaryVersion: "test", BuildIdentity: wingwire.BuildIdentity, Draining: true})
	}))
	_, err := EnsureDaemon(ctx, Options{Home: home, Version: "test", Spawn: func(string, string) error { t.Error("spawned over a stopping controller"); return nil }})
	var failure *apierr.Error
	if !errors.Is(err, context.Canceled) || !errors.As(err, &failure) || failure.Message != "the local controller is still stopping" || !strings.Contains(failure.Hint, "sparkwing daemon status") {
		t.Fatalf("wait on a stopping controller = %v", err)
	}
}
