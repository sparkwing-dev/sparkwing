package launcher

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

type boundaryFake struct {
	reservations int
	releases     int
	err          error
}

func (b *boundaryFake) Reserve(_ context.Context, a CloudJobBinding, name string) (CloudNodeReservation, CloudNetworkEvidence, error) {
	b.reservations++
	return CloudNodeReservation{NodeName: "node", NodeUID: "uid", Team: a.Team, JobName: name, ReservationID: "reservation"}, CloudNetworkEvidence{Namespace: "cloud", NodeUID: "uid", PolicyRevision: "revision", DefaultDenyEgressEnforced: true, RequiredDestinationsAllowed: true, DaemonSetPortsInventoried: true, EscapeProbePassed: true}, nil
}

func (b *boundaryFake) Release(context.Context, CloudNodeReservation) error {
	b.releases++
	return b.err
}

type controlFake struct {
	award  *CloudJobAward
	remove bool
	claims int
	fail   error
}

func (c *controlFake) Claim(context.Context) (*CloudJobAward, error) {
	c.claims++
	return c.award, c.fail
}

func (c *controlFake) Sync(_ context.Context, observations []CloudJobObservation) ([]CloudJobState, error) {
	states := make([]CloudJobState, len(observations))
	for i, b := range observations {
		states[i] = CloudJobState{Binding: b.Binding, Delete: c.remove}
	}
	return states, c.fail
}

func fixture() (*Launcher, *fake.Clientset, *boundaryFake, *controlFake) {
	now := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	image := "registry/runner@sha256:" + strings.Repeat("a", 64)
	control := &controlFake{award: &CloudJobAward{Binding: CloudJobBinding{UnitID: "unit", Kind: CloudWork, Team: "team", RunID: "run", NodeID: "job", Holder: "holder", Generation: 1, Attempt: 1, OwnerDeadline: now.Add(time.Hour), AttemptDeadline: now, ParentCapabilityExpiry: now.Add(2 * time.Hour)}, Bootstrap: []byte("bootstrap-secret"), CPUCores: 1.5, Memory: 2 << 30, Engine: &engine.Identity{Version: "v1.2.3", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.2.3"}, ManifestDigest: strings.Repeat("a", 64)}}}
	boundary := &boundaryFake{}
	kube := fake.NewClientset()
	l := &Launcher{Kube: kube, Control: control, Boundary: boundary, Config: CloudJobConfig{Namespace: "cloud", RunnerImage: image, DockerImage: image, ControllerURL: "https://controller.example", CPUCeiling: 4, MemoryCeiling: 8 << 30}}
	return l, kube, boundary, control
}

func TestLauncherCreatesSecretBeforeAllowingExecution(t *testing.T) {
	l, kube, _, _ := fixture()
	claimed, err := l.LaunchOne(t.Context())
	if err != nil || !claimed {
		t.Fatalf("launch=%v,%v", claimed, err)
	}
	jobs, err := kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs.Items) != 1 || *jobs.Items[0].Spec.Suspend {
		t.Fatal("Job did not start")
	}
	actions := kube.Actions()
	secretIndex, activateIndex := -1, -1
	for i, a := range actions {
		if a.GetResource().Resource == "secrets" && a.GetVerb() == "create" {
			secretIndex = i
		}
		if a.GetResource().Resource == "jobs" && a.GetVerb() == "create" {
			if !*a.(ktesting.CreateAction).GetObject().(*batchv1.Job).Spec.Suspend {
				t.Fatal("Job was runnable before Secret creation")
			}
		}
		if a.GetResource().Resource == "jobs" && a.GetVerb() == "patch" {
			activateIndex = i
		}
		if a.GetResource().Resource == "secrets" && (a.GetVerb() == "get" || a.GetVerb() == "list") {
			t.Fatal("launcher read a Secret")
		}
	}
	if secretIndex < 0 || activateIndex < secretIndex {
		t.Fatalf("Secret/activation order = %d/%d", secretIndex, activateIndex)
	}
}

func TestSyncRetainsReservationWhileDockerLives(t *testing.T) {
	l, kube, boundary, control := fixture()
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	name := jobName(control.award.Binding)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "cloud", Labels: map[string]string{JobLabel: name}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, InitContainerStatuses: []corev1.ContainerStatus{{Name: "docker", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}, ContainerStatuses: []corev1.ContainerStatus{{Name: "run-node", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}}}
	if _, err := kube.CoreV1().Pods("cloud").Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	control.remove = true
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if boundary.releases != 0 {
		t.Fatal("released a node while Docker ran")
	}
	if _, err := kube.CoreV1().Secrets("cloud").Get(t.Context(), name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("bootstrap was not deleted: %v", err)
	}
	// safety: the fake API lacks finalizer semantics, so preserve the deleting Job as the real API does.
	resources, err := BuildJob(t.Context(), time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), l.Config, *control.award, boundary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kube.BatchV1().Jobs("cloud").Create(t.Context(), resources.Job, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodSucceeded
	pod.Status.InitContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}
	if _, err := kube.CoreV1().Pods("cloud").UpdateStatus(t.Context(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if boundary.releases != 1 {
		t.Fatalf("release count=%d", boundary.releases)
	}
}

func TestLauncherCleansFailedCreation(t *testing.T) {
	for _, resource := range []string{"jobs", "secrets"} {
		t.Run(resource, func(t *testing.T) {
			l, kube, boundary, _ := fixture()
			kube.PrependReactor("create", resource, func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "test", errors.New("denied"))
			})
			if claimed, err := l.LaunchOne(t.Context()); !claimed || err == nil {
				t.Fatalf("launch=%v,%v", claimed, err)
			}
			if boundary.releases != 1 {
				t.Fatalf("reservation leaked after failed %s creation", resource)
			}
			secrets, err := kube.CoreV1().Secrets("cloud").List(t.Context(), metav1.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(secrets.Items) != 0 {
				t.Fatal("bootstrap leaked")
			}
		})
	}
}

func TestLauncherDeletesBootstrapWhenJobDeletionFails(t *testing.T) {
	l, kube, boundary, control := fixture()
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("Job deletion unavailable")
	kube.PrependReactor("delete", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, failure
	})
	control.remove = true
	if err := l.Sync(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("Job deletion failure lost: %v", err)
	}
	name := jobName(control.award.Binding)
	if _, err := kube.CoreV1().Secrets("cloud").Get(t.Context(), name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("bootstrap survived failed Job deletion: %v", err)
	}
	if _, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), name, metav1.GetOptions{}); err != nil {
		t.Fatalf("lost teardown ownership: %v", err)
	}
	if boundary.releases != 0 {
		t.Fatal("node released before Job deletion")
	}
}

func TestLauncherTeardownPreservesReplacementBootstrap(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		l, kube, boundary, control := fixture()
		kube.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
			action.(ktesting.CreateAction).GetObject().(*batchv1.Job).UID = "original"
			return false, nil, nil
		})
		if _, err := l.LaunchOne(t.Context()); err != nil {
			t.Fatal(err)
		}
		original, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), jobName(control.award.Binding), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		replacement := original.DeepCopy()
		replacement.UID = "replacement"
		if err := kube.Tracker().Update(batchv1.SchemeGroupVersion.WithResource("jobs"), replacement, "cloud"); err != nil {
			t.Fatal(err)
		}
		secret, err := kube.CoreV1().Secrets("cloud").Get(t.Context(), original.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		secret.OwnerReferences[0].UID = replacement.UID
		if err := kube.Tracker().Update(corev1.SchemeGroupVersion.WithResource("secrets"), secret, "cloud"); err != nil {
			t.Fatal(err)
		}
		failure := errors.New("Job delete failed")
		if conflict {
			failure = apierrors.NewConflict(schema.GroupResource{Resource: "jobs"}, original.Name, failure)
		}
		kube.PrependReactor("delete", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
			options := action.(ktesting.DeleteAction).GetDeleteOptions()
			if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != original.UID {
				t.Fatal("missing Job UID fence")
			}
			return true, nil, failure
		})
		if err := l.teardown(t.Context(), original, CloudNodeReservation{}); !errors.Is(err, failure) {
			t.Fatalf("deletion failure lost: %v", err)
		}
		if _, err := kube.CoreV1().Secrets("cloud").Get(t.Context(), original.Name, metav1.GetOptions{}); err != nil {
			t.Fatalf("replacement bootstrap removed: %v", err)
		}
		if boundary.releases != 0 {
			t.Fatal("stale reservation released")
		}
	}
}

func TestSyncAdoptsLiveJobWithoutCreatingAnother(t *testing.T) {
	l, kube, boundary, control := fixture()
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted := &Launcher{Kube: kube, Control: control, Boundary: boundary, Config: l.Config}
	kube.ClearActions()
	if err := restarted.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, a := range kube.Actions() {
		if a.GetVerb() == "create" || a.GetVerb() == "delete" {
			t.Fatal("live Job was replaced during reconstruction")
		}
	}
	if control.claims != 1 || boundary.releases != 0 {
		t.Fatal("reconstruction changed claim ownership")
	}
}

func TestSizingBelowCeiling(t *testing.T) {
	l, _, boundary, control := fixture()
	r, err := BuildJob(t.Context(), time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), l.Config, *control.award, boundary)
	if err != nil {
		t.Fatal(err)
	}
	resources := r.Job.Spec.Template.Spec.Resources
	if resources.Limits.Cpu().MilliValue() != 1500 || resources.Limits.Memory().Value() != 2<<30 {
		t.Fatalf("under-ceiling resources changed: %v", resources)
	}
}

func TestBuildJobRefusesOverCeilingBeforeReservation(t *testing.T) {
	for _, test := range []struct {
		name    string
		cpu     float64
		ceiling float64
		memory  int64
		want    []string
	}{
		{name: "cpu", cpu: 4.5, memory: 2 << 30, want: []string{"CPU", "4.5", "4", "ceiling"}},
		{name: "cpu_precision", cpu: 3.9995, ceiling: 3.9995, memory: 2 << 30, want: []string{"CPU", "4", "3.9995", "ceiling"}},
		{name: "cpu_nano_precision", cpu: 1.0009999999, ceiling: 1.0009999999, memory: 2 << 30, want: []string{"CPU", "1001m", "1.0009999999", "ceiling"}},
		{name: "memory", cpu: 1.5, memory: 9 << 30, want: []string{"memory", "9663676416", "8589934592", "ceiling"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			l, _, boundary, control := fixture()
			if test.ceiling > 0 {
				l.Config.CPUCeiling = test.ceiling
			}
			control.award.CPUCores, control.award.Memory = test.cpu, test.memory
			_, err := BuildJob(t.Context(), time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), l.Config, *control.award, boundary)
			if err == nil {
				t.Fatal("over-ceiling award silently changed resources")
			}
			for _, word := range test.want {
				if !strings.Contains(err.Error(), word) {
					t.Fatalf("missing %q in %v", word, err)
				}
			}
			if boundary.reservations != 0 {
				t.Fatal("invalid award reserved a Cloud node")
			}
		})
	}
}

func TestBuildJobRoundsFractionalCPUUpToKubernetesPrecision(t *testing.T) {
	for _, test := range []struct {
		cpu, ceiling float64
		milli        int64
	}{
		{1.2345, 4, 1235},
		{2.007, 4, 2007},
		{4.001, 4.001, 4001},
	} {
		l, _, boundary, control := fixture()
		control.award.CPUCores, l.Config.CPUCeiling = test.cpu, test.ceiling
		resources, err := BuildJob(t.Context(), time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), l.Config, *control.award, boundary)
		if err != nil {
			t.Fatal(err)
		}
		if got := resources.Job.Spec.Template.Spec.Resources.Limits.Cpu().MilliValue(); got != test.milli {
			t.Fatalf("CPU %g=%dm, want %dm", test.cpu, got, test.milli)
		}
	}
}
