package acceptance_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

type cloudControlW8 struct {
	award  *launcher.CloudJobAward
	remove bool
}

func TestLauncherRefusesAdmittedNodeChangeW8Components(t *testing.T) {
	for _, kind := range []launcher.CloudUnitKind{launcher.CloudPlan, launcher.CloudEval, launcher.CloudWork} {
		t.Run(string(kind), func(t *testing.T) {
			_, cfg, award := cloudFixture(kind)
			kube := cloudKubeW8()
			kube.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
				action.(ktesting.CreateAction).GetObject().(*batchv1.Job).Spec.Template.Spec.NodeName = "unreserved"
				return false, nil, nil
			})
			boundary := &cloudBoundaryFake{}
			l := &launcher.Launcher{Kube: kube, Control: &cloudControlW8{award: &award}, Boundary: boundary, Config: cfg}
			if claimed, err := l.LaunchOne(t.Context()); !claimed || err == nil {
				t.Fatalf("changed node admitted: claimed=%t err=%v", claimed, err)
			}
			for _, action := range kube.Actions() {
				if action.GetVerb() == "create" && action.GetResource().Resource == "secrets" {
					t.Fatal("bootstrap published for an unreserved node")
				}
			}
			if boundary.released != 1 {
				t.Fatalf("reservation releases=%d, want one", boundary.released)
			}
		})
	}
}

func (c *cloudControlW8) Claim(context.Context) (*launcher.CloudJobAward, error) {
	return c.award, nil
}

func (c *cloudControlW8) Sync(_ context.Context, bindings []launcher.CloudJobObservation) ([]launcher.CloudJobState, error) {
	states := make([]launcher.CloudJobState, len(bindings))
	for i, binding := range bindings {
		states[i] = launcher.CloudJobState{Binding: binding.Binding, Delete: c.remove}
	}
	return states, nil
}

func TestLauncherKubernetesBoundaryW8Components(t *testing.T) {
	for _, kind := range []launcher.CloudUnitKind{launcher.CloudPlan, launcher.CloudEval, launcher.CloudWork} {
		t.Run(string(kind), func(t *testing.T) {
			_, cfg, award := cloudFixture(kind)
			control := &cloudControlW8{award: &award}
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "dedicated", UID: "node-uid", Labels: map[string]string{launcher.TeamNodeLabel: launcher.TeamNodeValue("cloud", "team")}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
			karpenter := cloudProvisionerW8(node, "team")
			kube := cloudKubeW8(node)
			// safety: retain deleting Jobs so the fake API exercises reconstruction while DinD drains.
			kube.PrependReactor("delete", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
				name := action.(ktesting.DeleteAction).GetName()
				gvr := batchv1.SchemeGroupVersion.WithResource("jobs")
				object, err := kube.Tracker().Get(gvr, cfg.Namespace, name)
				if err != nil {
					return true, nil, err
				}
				job := object.(*batchv1.Job).DeepCopy()
				preconditions := action.(ktesting.DeleteAction).GetDeleteOptions().Preconditions
				if preconditions == nil || preconditions.UID == nil || *preconditions.UID != job.UID {
					t.Fatal("cleanup lost its observed Job UID precondition")
				}
				now := metav1.Now()
				job.DeletionTimestamp = &now
				return true, nil, kube.Tracker().Update(gvr, job, cfg.Namespace)
			})
			clears := 0
			boundary := &launcher.KubernetesBoundary{
				Kube: kube, Karpenter: karpenter, Namespace: cfg.Namespace,
				Probe: func(_ context.Context, r launcher.CloudNodeReservation) (launcher.CloudNetworkEvidence, error) {
					return launcher.CloudNetworkEvidence{Namespace: cfg.Namespace, NodeUID: r.NodeUID, PolicyRevision: "tested-policy", DefaultDenyEgressEnforced: true, RequiredDestinationsAllowed: true, DaemonSetPortsInventoried: true, EscapeProbePassed: true}, nil
				},
				Clear: func(context.Context, launcher.CloudNodeReservation) error { clears++; return nil },
			}
			first := &launcher.Launcher{Kube: kube, Control: control, Boundary: boundary, Config: cfg}
			if claimed, err := first.LaunchOne(t.Context()); err != nil || !claimed {
				t.Fatalf("launch: %t %v", claimed, err)
			}
			jobs, err := kube.BatchV1().Jobs(cfg.Namespace).List(t.Context(), metav1.ListOptions{})
			if err != nil || len(jobs.Items) != 1 {
				t.Fatalf("jobs: %+v %v", jobs, err)
			}
			job := jobs.Items[0]
			if _, _, err := boundary.Reserve(t.Context(), award.Binding, "competing-job"); !errors.Is(err, launcher.ErrNoDedicatedNode) {
				t.Fatalf("occupied node reused: %v", err)
			}
			pod := &corev1.Pod{ObjectMeta: *job.Spec.Template.ObjectMeta.DeepCopy(), Spec: job.Spec.Template.Spec, Status: corev1.PodStatus{Phase: corev1.PodRunning, InitContainerStatuses: []corev1.ContainerStatus{{Name: "docker", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
			pod.Name, pod.Namespace = "work", cfg.Namespace
			controller := true
			pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: &controller, BlockOwnerDeletion: &controller}}
			if _, err := kube.CoreV1().Pods(cfg.Namespace).Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			reconstructedBoundary := *boundary
			restarted := restartedLauncherW8(t, kube, &reconstructedBoundary, cfg, award, control)
			if err := restarted.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			control.remove = true
			if err := restarted.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			if clears != 0 {
				t.Fatal("node cleaned before Docker stopped")
			}
			if _, err := kube.CoreV1().Secrets(cfg.Namespace).Get(t.Context(), job.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatalf("bootstrap survived cancellation: %v", err)
			}
			if err := kube.CoreV1().Pods(cfg.Namespace).Delete(t.Context(), pod.Name, metav1.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := restarted.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			if clears != 1 {
				t.Fatalf("reservation cleanup calls=%d, want one", clears)
			}
			finished, err := kube.BatchV1().Jobs(cfg.Namespace).Get(t.Context(), job.Name, metav1.GetOptions{})
			if err != nil || slices.Contains(finished.Finalizers, "sparkwing.dev/cloud-cleanup") {
				t.Fatalf("teardown finalizer: %+v %v", finished, err)
			}
			if _, _, err := reconstructedBoundary.Reserve(t.Context(), award.Binding, "next-job"); err != nil {
				t.Fatalf("clean node could not be reused: %v", err)
			}
		})
	}
}

func restartedLauncherW8(t *testing.T, kube *fake.Clientset, boundary launcher.CloudBoundary, cfg launcher.CloudJobConfig, award launcher.CloudJobAward, control *cloudControlW8) *launcher.Launcher {
	t.Helper()
	b := award.Binding
	unit := controllerwire.ClaimedUnit{
		Unit: capability.UnitNode, Purpose: capability.PurposeWork, Delivery: capability.DeliveryBootstrap,
		UnitID: b.UnitID, RunID: b.RunID, JobID: b.NodeID,
		Binding:       capability.Binding{Kind: capability.BindingNode, RunID: b.RunID, NodeID: b.NodeID, Holder: b.Holder, Generation: b.Generation, Attempt: b.Attempt},
		Request:       runspec.SealedRequest{RunID: b.RunID, Request: runspec.Request{Team: b.Team}},
		OwnerDeadline: &b.OwnerDeadline, AttemptDeadline: &b.AttemptDeadline, ParentCapabilityExpiry: &b.ParentCapabilityExpiry,
	}
	switch b.Kind {
	case launcher.CloudPlan:
		unit.Unit, unit.Purpose, unit.Binding.Kind = capability.UnitPlan, capability.PurposePlan, capability.BindingRun
	case launcher.CloudEval:
		unit.Unit, unit.Purpose, unit.SourceGeneration = capability.UnitEval, capability.PurposeEval, b.SourceGeneration
		unit.Binding.Generation = b.SourceGeneration
		unit.Binding.Expansion = &capability.LeaseRef{Kind: capability.UnitEval, ID: b.UnitID, Holder: b.Holder, Generation: b.Generation}
	}
	transport := &http.Client{}
	restarted, err := launcher.NewLauncher(t.Context(), launcher.RuntimeConfig{
		Kube: kube, Boundary: boundary, Jobs: cfg, HTTP: transport,
		Demand: controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{unit.Unit}},
		Bearer: func(context.Context) (string, error) { return "launcher-root", nil },
		NewController: func(options client.EngineOptions) (launcher.LauncherController, error) {
			bearer, err := options.Bearer(t.Context())
			if err != nil || bearer != "launcher-root" || options.HTTP != transport || options.URL != cfg.ControllerURL || !options.Launcher {
				t.Fatal("restart lost the shared launcher transport")
			}
			return launcherControllerW8{
				claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
					t.Fatal("restart attempted to replace an observed award")
					return controllerwire.ClaimResponse{}, nil
				},
				sync: func(_ context.Context, request controllerwire.LauncherSyncRequest) (controllerwire.LauncherSyncResponse, error) {
					if len(request.Observed) != 1 {
						t.Fatalf("restart observed %d Jobs", len(request.Observed))
					}
					observation := request.Observed[0]
					wantAward := capability.LeaseRef{Kind: unit.Unit, ID: b.UnitID, Holder: b.Holder, Generation: b.Generation}
					if observation.Award != wantAward || observation.NodeUID != "node-uid" || observation.Job.Namespace != cfg.Namespace || observation.Job.UID != "job-uid" {
						t.Fatal("restart lost persisted unit or node identity")
					}
					if control.remove {
						return controllerwire.LauncherSyncResponse{Remove: []controllerwire.LauncherJobRef{observation.Job}}, nil
					}
					return controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{unit}}, nil
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return restarted
}

func (c *cloudControlW8) Prepare(ctx context.Context, _ []capability.LeaseRef) (time.Time, error) {
	return time.Now().Add(time.Minute), context.Cause(ctx)
}
