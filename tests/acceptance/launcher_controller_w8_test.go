package acceptance_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type launcherControllerW8 struct {
	claim func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error)
	sync  func(context.Context, controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error)
}

func (c launcherControllerW8) Claim(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return c.claim(ctx, request)
}

func (c launcherControllerW8) SyncLauncher(ctx context.Context, request controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
	return c.sync(ctx, request)
}

func TestLauncherControllerW8Components(t *testing.T) {
	now, config, award := cloudFixture(launcher.CloudWork)
	boundary := &cloudBoundaryFake{}
	resources, err := launcher.BuildJob(t.Context(), now, config, award, boundary)
	if err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientset(resources.Job, resources.BootstrapSecret)
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
			want := controllerwire.LauncherObservation{UnitID: award.Binding.UnitID, JobName: resources.Job.Name, NodeUID: resources.Reservation.NodeUID}
			if !slices.Equal(request.Observed, []controllerwire.LauncherObservation{want}) {
				t.Fatalf("restart lost persisted observation: %+v", request.Observed)
			}
			if transportErr != nil {
				return controllerwire.LauncherSyncResponse{}, transportErr
			}
			if remove {
				return controllerwire.LauncherSyncResponse{Remove: []string{resources.Job.Name}}, nil
			}
			b := award.Binding
			return controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{{
				Unit: capability.UnitNode, Purpose: capability.PurposeWork, Delivery: capability.DeliveryBootstrap,
				UnitID: b.UnitID, RunID: b.RunID, JobID: b.NodeID,
				Binding:       capability.Binding{Kind: capability.BindingNode, RunID: b.RunID, NodeID: b.NodeID, Holder: b.Holder, Generation: b.Generation, Attempt: b.Attempt},
				Request:       runspec.SealedRequest{RunID: b.RunID, Request: runspec.Request{Team: b.Team}},
				OwnerDeadline: &b.OwnerDeadline, AttemptDeadline: &b.AttemptDeadline, ParentCapabilityExpiry: &b.ParentCapabilityExpiry,
			}}}, nil
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
	if err != nil || len(secrets.Items) != 0 {
		t.Fatalf("retirement retained a bootstrap: %v", err)
	}
}
