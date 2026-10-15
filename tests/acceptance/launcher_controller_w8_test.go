package acceptance_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/cluster"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type launcherControllerW8 struct {
	controllerwire.Controller
	claim func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error)
	sync  func(context.Context, controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error)
}

func (c launcherControllerW8) Claim(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return c.claim(ctx, request)
}

func (c launcherControllerW8) SyncLauncher(ctx context.Context, request controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
	return c.sync(ctx, request)
}

func TestLauncherPollingW8Components(t *testing.T) {
	for _, option := range []string{"--poll=1s", ""} {
		t.Run(option, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Hour)
				defer cancel()
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "agent-token"), []byte("fixture-launcher-root"), 0o600); err != nil {
					t.Fatal(err)
				}
				_, config, _ := cloudFixture(launcher.CloudWork)
				var claims []time.Time
				control, err := launcher.NewControllerControl(launcherControllerW8{
					claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
						claims = append(claims, time.Now())
						if len(claims) == 2 {
							cancel()
						}
						return controllerwire.ClaimResponse{}, nil
					},
				}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}})
				if err != nil {
					t.Fatal(err)
				}
				args := []string{"--controller=" + config.ControllerURL, "--namespace=" + config.Namespace, "--image=" + config.RunnerImage, "--docker-image=" + config.DockerImage, "--cpu-ceiling=4", "--memory-ceiling=8Gi", "--credentials-dir=" + dir}
				want := time.Hour
				if option != "" {
					args = append(args, option)
					want = time.Second
				}
				err = cluster.RunLaunchCLI(ctx, args, func(_ context.Context, cfg launcher.CloudJobConfig, _ string) (*launcher.Launcher, error) {
					return &launcher.Launcher{Config: cfg, Kube: fake.NewClientset(), Control: control, Boundary: &cloudBoundaryFake{}, Poll: time.Hour}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if len(claims) != 2 || claims[1].Sub(claims[0]) != want {
					t.Fatalf("launcher claim times = %v, want two claims %v apart", claims, want)
				}
			})
		})
	}
}

func TestLauncherPollingRefusesInvalidW8Components(t *testing.T) {
	for _, value := range []string{"0s", "-1s", "invalid", ""} {
		t.Run(value, func(t *testing.T) {
			err := cluster.RunLaunchCLI(t.Context(), []string{"--poll=" + value}, func(context.Context, launcher.CloudJobConfig, string) (*launcher.Launcher, error) {
				t.Fatal("invalid poll reached launcher construction")
				return nil, nil
			})
			if err == nil || !strings.Contains(err.Error(), "--poll") {
				t.Fatalf("poll validation error = %v", err)
			}
		})
	}
}

func TestLauncherControllerW8Components(t *testing.T) {
	now, config, award := cloudFixture(launcher.CloudWork)
	boundary := &cloudBoundaryFake{}
	resources, err := launcher.BuildJob(t.Context(), now, config, award, boundary)
	if err != nil {
		t.Fatal(err)
	}
	second := award
	second.Binding.UnitID += "-second"
	second.Binding.NodeID += "-second"
	other, err := launcher.BuildJob(t.Context(), now, config, second, boundary)
	if err != nil {
		t.Fatal(err)
	}
	resources.Job.UID, other.Job.UID = "first-job-uid", "second-job-uid"
	kube := fake.NewClientset(resources.Job, resources.BootstrapSecret, other.Job, other.BootstrapSecret)
	demand := controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}, AvailableCores: 4, AvailableMemoryBytes: 8 << 30}
	remove := false
	transportErr := error(nil)
	wire := launcherControllerW8{
		claim: func(_ context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			if !slices.Equal(request.Kinds, demand.Kinds) || request.RunnerVersion != demand.RunnerVersion || request.AvailableCores != demand.AvailableCores || request.AvailableMemoryBytes != demand.AvailableMemoryBytes {
				t.Fatalf("launcher split or changed its claim: %+v", request)
			}
			return controllerwire.ClaimResponse{}, nil
		},
		sync: func(_ context.Context, request controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
			want := []controllerwire.LauncherObservation{
				{Award: capability.LeaseRef{Kind: capability.UnitNode, ID: award.Binding.UnitID, Holder: award.Binding.Holder, Generation: award.Binding.Generation}, Job: controllerwire.LauncherJobRef{Namespace: resources.Job.Namespace, Name: resources.Job.Name, UID: string(resources.Job.UID)}, NodeUID: resources.Reservation.NodeUID},
				{Award: capability.LeaseRef{Kind: capability.UnitNode, ID: second.Binding.UnitID, Holder: second.Binding.Holder, Generation: second.Binding.Generation}, Job: controllerwire.LauncherJobRef{Namespace: other.Job.Namespace, Name: other.Job.Name, UID: string(other.Job.UID)}, NodeUID: other.Reservation.NodeUID},
			}
			slices.SortFunc(want, func(a, b controllerwire.LauncherObservation) int { return strings.Compare(a.Job.Name, b.Job.Name) })
			if !slices.Equal(request.Observed, want) {
				t.Fatalf("restart lost persisted observation: %+v", request.Observed)
			}
			if transportErr != nil {
				return controllerwire.LauncherSyncResponse{}, transportErr
			}
			response := controllerwire.LauncherSyncResponse{}
			for _, binding := range []launcher.CloudJobBinding{second.Binding, award.Binding} {
				if remove && binding.UnitID == award.Binding.UnitID {
					response.Remove = append(response.Remove, controllerwire.LauncherJobRef{Namespace: resources.Job.Namespace, Name: resources.Job.Name, UID: string(resources.Job.UID)})
					continue
				}
				b := binding
				response.Awards = append(response.Awards, controllerwire.ClaimedUnit{
					Unit: capability.UnitNode, Purpose: capability.PurposeWork, Delivery: capability.DeliveryBootstrap,
					UnitID: b.UnitID, RunID: b.RunID, JobID: b.NodeID,
					Binding:       capability.Binding{Kind: capability.BindingNode, RunID: b.RunID, NodeID: b.NodeID, Holder: b.Holder, Generation: b.Generation, Attempt: b.Attempt},
					Request:       runspec.SealedRequest{RunID: b.RunID, Request: runspec.Request{Team: b.Team}},
					OwnerDeadline: &b.OwnerDeadline, AttemptDeadline: &b.AttemptDeadline, ParentCapabilityExpiry: &b.ParentCapabilityExpiry,
				})
			}
			return response, nil
		},
	}
	control, err := launcher.NewControllerControl(wire, demand)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := control.Claim(t.Context()); err != nil || got != nil {
		t.Fatalf("empty claim=%v err=%v", got, err)
	}
	restarted := launcher.Launcher{Control: control, Kube: kube, Boundary: boundary, Config: config}
	if err := restarted.Sync(t.Context()); err != nil || boundary.released != 0 {
		t.Fatalf("restart retired a live award: %v", err)
	}
	transportErr = errors.New("controller unavailable")
	if err := restarted.Sync(t.Context()); !errors.Is(err, transportErr) || boundary.released != 0 {
		t.Fatalf("transport failure lost teardown ownership: %v", err)
	}
	transportErr, remove = nil, true
	if err := restarted.Sync(t.Context()); err != nil || boundary.released != 1 {
		t.Fatalf("retirement failed: released=%d err=%v", boundary.released, err)
	}
	secrets, err := kube.CoreV1().Secrets(config.Namespace).List(t.Context(), metav1.ListOptions{})
	if err != nil || len(secrets.Items) != 1 || secrets.Items[0].Name != other.BootstrapSecret.Name {
		t.Fatalf("retirement did not preserve only the live bootstrap: %v", err)
	}
}
