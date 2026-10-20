package launcher

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestPoolClaimsLaunchFreshPodsWithoutThePoolCredential(t *testing.T) {
	base, kube, boundary, _ := fixture()
	unit := wireAward(capability.UnitNode)
	unit.Runner.Type = "runner"
	unit.CloudResources = &match.Resources{Cores: 1}
	l, err := NewLauncher(t.Context(), RuntimeConfig{
		Kube: kube, Boundary: boundary, Jobs: base.Config,
		Demand: controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{capability.UnitNode}, AvailableCores: 4, AvailableMemoryBytes: 8 << 30},
		Bearer: func(context.Context) (string, error) { return "pool-root", nil },
		NewController: func(options client.EngineOptions) (LauncherController, error) {
			if token, err := options.Bearer(t.Context()); err != nil || token != "pool-root" || !options.Launcher {
				t.Fatalf("pool transport: %q %v", token, err)
			}
			return launcherWireFake{sync: func(_ context.Context, request controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
				response := controllerwire.LauncherSyncResponse{}
				for _, fence := range request.Preparing {
					response.Prepared = append(response.Prepared, controllerwire.LauncherPreparation{LeaseRef: fence, ExpiresAt: time.Now().Add(time.Minute)})
				}
				for _, observed := range request.Observed {
					copy := unit
					copy.Binding.Generation = observed.Award.Generation
					copy.Capability = ""
					response.Awards = append(response.Awards, copy)
				}
				return response, nil
			}, claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				unit.Binding.Generation++
				copy := unit
				return controllerwire.ClaimResponse{Claim: &copy}, nil
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if launched, err := l.LaunchOne(t.Context()); err != nil || !launched {
			t.Fatalf("pool launch: %v %v", launched, err)
		}
	}
	jobs, err := kube.BatchV1().Jobs(base.Config.Namespace).List(t.Context(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 2 || jobs.Items[0].Name == jobs.Items[1].Name {
		t.Fatalf("claims reused a Job: %v %v", jobs, err)
	}
	for _, job := range jobs.Items {
		pod := job.Spec.Template.Spec
		if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken || pod.ServiceAccountName != ServiceAccount {
			t.Fatal("job can reach the launcher's Kubernetes identity")
		}
		for _, volume := range pod.Volumes {
			if volume.EmptyDir == nil && (volume.Secret == nil || volume.Secret.SecretName != job.Name) {
				t.Fatalf("job mounted reusable state or pool authority: %+v", volume)
			}
		}
	}
}
