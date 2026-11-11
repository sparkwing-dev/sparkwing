//go:build unix

package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
)

type shutdownPlacementBoundary struct {
	CloudBoundary
	fail func() error
}

func (b shutdownPlacementBoundary) Reserve(context.Context, CloudJobBinding, string, CloudPlacement) (CloudNodeReservation, error) {
	return CloudNodeReservation{}, b.fail()
}

func TestLauncherSIGTERMAtEveryPhase(t *testing.T) {
	for _, phase := range []string{"claim", "placement", "warm", "reconcile", "teardown", "retirement", "budget"} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/joined=%v", phase, mixed), func(t *testing.T) {
				l, kube, boundary, _ := fixture()
				if phase == "reconcile" || phase == "teardown" {
					if launched, err := l.LaunchOne(t.Context()); err != nil || !launched {
						t.Fatalf("seed Job: %v %v", launched, err)
					}
				}
				ctx, stop := signal.NotifyContext(t.Context(), syscall.SIGTERM)
				defer stop()
				var timedOut atomic.Bool
				guard := time.AfterFunc(5*time.Second, func() { timedOut.Store(true); stop() })
				defer guard.Stop()
				var hits, requests atomic.Int64
				fail := func() error {
					hits.Add(1)
					if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
						return err
					}
					<-ctx.Done()
					if mixed {
						return errors.Join(ctx.Err(), errors.New("independent shutdown fault"))
					}
					return fmt.Errorf("interrupted operation: %w", ctx.Err())
				}
				encode := func(value any) *http.Response {
					body, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					return claimPollResponse(200, string(body))
				}
				unit := wireAward(capability.UnitNode)
				wire, err := client.NewEngineClient(client.EngineOptions{URL: "http://controller.invalid", Launcher: true, HTTP: &http.Client{Transport: cloudResourceTransport(func(r *http.Request) (*http.Response, error) {
					requests.Add(1)
					if r.URL.Path == "/api/v1/launcher/claim" {
						if phase == "claim" {
							return nil, fail()
						}
						if phase == "placement" || phase == "budget" {
							return encode(controllerwire.ClaimResponse{Claim: &unit}), nil
						}
						return claimPollResponse(200, `{}`), nil
					}
					var request controllerwire.LauncherSyncRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						return nil, err
					}
					if phase == "warm" && request.WarmPools != nil || phase == "reconcile" && len(request.Observed) > 0 {
						return nil, fail()
					}
					response := controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{}, Retired: []capability.LeaseRef{}}
					for _, fence := range request.Preparing {
						response.Prepared = append(response.Prepared, controllerwire.LauncherPreparation{LeaseRef: fence, ExpiresAt: time.Now().Add(time.Minute)})
					}
					for _, observation := range request.Observed {
						response.Retired = append(response.Retired, observation.Award)
					}
					return encode(response), nil
				})}})
				if err != nil {
					t.Fatal(err)
				}
				control, err := NewControllerControl(wire, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}})
				if err != nil {
					t.Fatal(err)
				}
				l.Control = control
				switch phase {
				case "placement":
					l.Boundary = shutdownPlacementBoundary{CloudBoundary: boundary, fail: fail}
				case "teardown":
					kube.PrependReactor("delete", "jobs", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, fail() })
				case "retirement":
					// safety: warm the real client before retirement can interrupt the concurrently starting Run loop.
					if err := control.ReportWarmPools(ctx, map[string]int64{}); err != nil {
						t.Fatal(err)
					}
					l.Boundary = &KubernetesBoundary{Kube: kube, Namespace: l.Config.Namespace, NodeClassName: l.Config.NodeClassName}
					kube.PrependReactor("list", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
						if strings.Contains(action.(ktesting.ListAction).GetListRestrictions().Labels.String(), usedRunLabel) {
							return true, nil, fail()
						}
						return false, nil, nil
					})
				case "budget":
					boundary.reserveErr = ErrNoDedicatedNode
					l.Breaker = func(context.Context) (bool, error) { return false, fail() }
				}
				var logs strings.Builder
				l.Logger = slog.New(slog.NewTextHandler(&logs, nil))
				if err := l.Run(ctx); err != nil {
					t.Fatal(err)
				}
				noisy := strings.Contains(logs.String(), "level=WARN") || strings.Contains(logs.String(), "level=ERROR")
				if timedOut.Load() || hits.Load() != 1 || requests.Load() == 0 || noisy != mixed {
					t.Fatalf("phase=%s hits=%d requests=%d noisy=%v want=%v logs=%s", phase, hits.Load(), requests.Load(), noisy, mixed, logs.String())
				}
				if mixed && !strings.Contains(logs.String(), "independent shutdown fault") {
					t.Fatal(logs.String())
				}
				if mixed && phase != "teardown" && !strings.Contains(logs.String(), "level=WARN") {
					t.Fatal(logs.String())
				}
				if mixed && phase == "teardown" && !strings.Contains(logs.String(), "level=ERROR") {
					t.Fatal(logs.String())
				}
			})
		}
	}
}
