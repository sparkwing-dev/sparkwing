package launcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestLauncherShutdownPreservesIndependentFaults(t *testing.T) {
	for _, phase := range []string{"claim", "warm report", "reconciliation", "retirement", "breaker"} {
		for _, kind := range []string{"wrapped cancellation", "joined cancellations", "parent deadline", "mixed fault", "unrelated fault", "live parent"} {
			t.Run(phase+"/"+kind, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					l, kube, boundary, control := fixture()
					ctx, cancel := context.WithCancel(t.Context())
					if kind == "parent deadline" {
						cancel()
						ctx, cancel = context.WithTimeout(t.Context(), time.Second)
					}
					defer cancel()
					calls := 0
					failure := func() error {
						calls++
						switch kind {
						case "live parent":
							time.AfterFunc(time.Second, cancel)
						case "parent deadline":
							time.Sleep(time.Second)
						default:
							cancel()
						}
						switch kind {
						case "joined cancellations":
							return fmt.Errorf("operation: %w", errors.Join(context.Canceled, fmt.Errorf("nested: %w", context.Canceled)))
						case "mixed fault":
							return fmt.Errorf("operation: %w", errors.Join(context.Canceled, errors.New("independent cleanup failure")))
						case "unrelated fault":
							return errors.New("independent cleanup failure")
						case "parent deadline":
							return fmt.Errorf("operation: %w", context.DeadlineExceeded)
						default:
							return fmt.Errorf("operation: %w", context.Canceled)
						}
					}
					award := control.award
					control.award = nil
					switch phase {
					case "claim", "warm report":
						path := "/api/v1/launcher/claim"
						if phase == "warm report" {
							path = "/api/v1/launcher/sync"
						}
						wire, err := client.NewEngineClient(client.EngineOptions{URL: "http://controller.invalid", Launcher: true, HTTP: &http.Client{Transport: cloudResourceTransport(func(r *http.Request) (*http.Response, error) {
							if r.URL.Path == path {
								return nil, failure()
							}
							return claimPollResponse(200, `{"awards":[],"retired":[]}`), nil
						})}})
						if err != nil {
							t.Fatal(err)
						}
						l.Control, err = NewControllerControl(wire, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}})
						if err != nil {
							t.Fatal(err)
						}
					case "reconciliation":
						kube.PrependReactor("list", "jobs", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, failure() })
					case "retirement":
						l.Boundary = &KubernetesBoundary{Kube: kube, Namespace: l.Config.Namespace, NodeClassName: l.Config.NodeClassName}
						kube.PrependReactor("list", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
							if strings.Contains(action.(ktesting.ListAction).GetListRestrictions().Labels.String(), usedRunLabel) {
								return true, nil, failure()
							}
							return false, nil, nil
						})
					case "breaker":
						control.award = award
						boundary.reserveErr = ErrNoDedicatedNode
						l.Breaker = func(context.Context) (bool, error) { return false, failure() }
					}
					var logs strings.Builder
					l.Poll, l.Logger = 2*time.Second, slog.New(slog.NewTextHandler(&logs, nil))
					if err := l.Run(ctx); err != nil {
						t.Fatal(err)
					}
					wantWarn := kind == "mixed fault" || kind == "unrelated fault" || kind == "live parent"
					warned := strings.Contains(logs.String(), "level=WARN") || strings.Contains(logs.String(), "level=ERROR")
					if calls != 1 || warned != wantWarn {
						t.Fatalf("calls=%d warn=%v want=%v logs=%s", calls, warned, wantWarn, logs.String())
					}
					if (kind == "mixed fault" || kind == "unrelated fault") && !strings.Contains(logs.String(), "independent cleanup failure") {
						t.Fatal(logs.String())
					}
				})
			})
		}
	}
}

func TestLauncherGateway503RemainsWarn(t *testing.T) {
	for _, path := range []string{"/api/v1/launcher/claim", "/api/v1/launcher/sync"} {
		t.Run(path, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l, _, _, _ := fixture()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				wire, err := client.NewEngineClient(client.EngineOptions{URL: "http://controller.invalid", Launcher: true, HTTP: &http.Client{Transport: cloudResourceTransport(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == path {
						cancel()
						return claimPollResponse(503, "bad gateway"), nil
					}
					return claimPollResponse(200, `{"awards":[],"retired":[]}`), nil
				})}})
				if err != nil {
					t.Fatal(err)
				}
				l.Control, err = NewControllerControl(wire, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}})
				if err != nil {
					t.Fatal(err)
				}
				var logs strings.Builder
				l.Logger = slog.New(slog.NewTextHandler(&logs, nil))
				if err := l.Run(ctx); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(logs.String(), "level=WARN") {
					t.Fatalf("gateway failure hidden: %s", logs.String())
				}
			})
		})
	}
}
