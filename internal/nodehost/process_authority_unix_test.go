//go:build unix

package nodehost

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestProtocolSessionUsesProcessAuthorityUntilClose(t *testing.T) {
	for _, mode := range []string{"environment", "mismatch", "missing"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			registry, _, lookup, spec, service := processAuthorityFixture(t)
			issued, revoke, err := registry.Delegate(t.Context(), lookup, spec, service)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = registry.Close() })
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cleaned := false
			cfg := ProtocolSessionConfig{
				Command: []string{exe, "-test.run=^TestProtocolSessionChild$", "--", mode}, Environment: []string{"GORACE=atexit_sleep_ms=0"},
				ParseFrame: protocolTestCodec, ValidateRequest: func(nodeproto.Request) error { return nil }, Revoke: revoke,
				RunCleanups: func(context.Context, CleanupScope) error {
					if _, err := registry.Resolve(t.Context(), processAuthorized(issued)); err == nil {
						t.Error("cleanup began with live process authority")
					}
					cleaned = true
					return nil
				},
			}
			if mode == "missing" {
				cfg.Command = []string{"/missing-sdk-entry"}
			}
			session, err := StartProtocolSession(ctx, cfg)
			if mode == "missing" {
				if err == nil || session != nil {
					t.Fatal("missing SDK started")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = session.Close() })
				hello := nodeproto.Request{Op: nodeproto.OpHello, Protocol: nodeproto.Version, Engine: &nodeproto.EngineIdentity{Version: "test"}, Runner: &nodeproto.RunnerIdentity{Name: "granted", Type: "local"}, Binding: &nodeproto.ProcessBinding{RunID: "run", JobID: "job", Routes: nodeproto.ProcessRoutes{URL: "http://host", Bearer: issued.Raw}}}
				_, helloErr := session.Request(ctx, hello)
				if (helloErr == nil) != (mode == "environment") {
					t.Fatalf("hello result=%v", helloErr)
				}
				if mode == "mismatch" {
					var mismatch *apierr.Error
					if !errors.As(helloErr, &mismatch) || mismatch.Code != apierr.ProtocolMismatch {
						t.Fatalf("hello mismatch=%v", helloErr)
					}
				}
				if mode == "environment" {
					reply, err := session.Request(ctx, nodeproto.Request{Op: nodeproto.OpDescribe})
					if err != nil || strings.Contains(string(reply.Result), issued.Raw) {
						t.Fatal("process bearer reached interpreter environment")
					}
					if _, err := registry.ReadAuthentication(t.Context(), processLookup(issued), capability.RequestEvidence{}); err != nil {
						t.Fatal("live session lost its process authority")
					}
				}
				closeErr := session.Close()
				if mode == "environment" && closeErr != nil {
					t.Fatal(closeErr)
				}
				if err := session.Cleanup(ctx); err != nil || !cleaned {
					t.Fatalf("cleanup=%v error=%v", cleaned, err)
				}
			}
			if _, err := registry.ReadAuthentication(t.Context(), processLookup(issued), capability.RequestEvidence{}); err == nil {
				t.Fatal("ended process retained its bearer")
			}
		})
	}
}
