package launcher

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"

	authenticationv1 "k8s.io/api/authentication/v1"

	batchv1 "k8s.io/api/batch/v1"

	corev1 "k8s.io/api/core/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

type boundaryFake struct {
	reservations int
	releases     int
	retirements  int
	err          error
	reserveErr   error
}

func (b *boundaryFake) Reserve(_ context.Context, a CloudJobBinding, name string) (CloudNodeReservation, error) {
	b.reservations++
	if b.reserveErr != nil {
		return CloudNodeReservation{}, b.reserveErr
	}
	return CloudNodeReservation{NodeName: "node", NodeUID: "uid", Team: a.Team, JobName: name, ReservationID: "reservation", Award: a.LeaseRef()}, nil
}

func (b *boundaryFake) Release(context.Context, CloudNodeReservation) error {
	b.releases++
	return b.err
}

func (b *boundaryFake) Retire(context.Context, CloudNodeReservation) error {
	b.retirements++
	return b.err
}

type controlFake struct {
	award    *CloudJobAward
	remove   bool
	claims   int
	fail     error
	excluded []string
}

func (c *controlFake) Claim(_ context.Context, _ string, excluded []string) (*CloudJobAward, error) {
	c.claims++
	c.excluded = append([]string(nil), excluded...)
	return c.award, c.fail
}

func (c *controlFake) Sync(_ context.Context, observations []CloudJobObservation) ([]CloudJobState, error) {
	states := make([]CloudJobState, len(observations))
	expires := time.Now().Add(time.Hour)
	if c.award != nil {
		expires = c.award.LeaseExpiresAt
	}
	for i, b := range observations {
		states[i] = CloudJobState{Binding: b.Binding, LeaseExpiresAt: expires, Delete: c.remove || b.Rejected}
	}
	return states, c.fail
}

func fixture() (*Launcher, *fake.Clientset, *boundaryFake, *controlFake) {
	now := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	image := "registry/runner@sha256:" + strings.Repeat("a", 64)
	control := &controlFake{award: &CloudJobAward{LeaseExpiresAt: now.Add(time.Hour), Binding: CloudJobBinding{UnitID: "unit", Kind: CloudWork, Team: "team", RunID: "run", NodeID: "job", Holder: "holder", Generation: 1, Attempt: 1, OwnerDeadline: now.Add(time.Hour), AttemptDeadline: now, ParentCapabilityExpiry: now.Add(2 * time.Hour)}, Bootstrap: []byte("bootstrap-secret"), CPUCores: 1.5, Memory: 2 << 30, Engine: &engineid.Identity{Version: "v1.2.3", Pin: engineid.Pin{SDK: engineid.GoSDK, Version: "v1.2.3"}, ManifestDigest: strings.Repeat("a", 64)}}}
	boundary := &boundaryFake{}
	kube := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cloud", Labels: map[string]string{"kubernetes.io/metadata.name": "cloud"}}})
	firstJobName := jobName(control.award.Binding)
	kube.PrependReactor("create", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		switch obj := action.(ktesting.CreateAction).GetObject().(type) {
		case *batchv1.Job:
			if obj.UID == "" {
				obj.UID = "job-uid"
				if obj.Name != firstJobName {
					obj.UID = types.UID(obj.Name + "-uid")
				}
			}
		case *corev1.Secret:
			if obj.UID == "" {
				obj.UID = "secret-uid"
			}
		}
		return false, nil, nil
	})
	retainFinalizedJobs(kube)
	kube.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		namespace := action.GetNamespace()
		pods, err := kube.Tracker().List(corev1.SchemeGroupVersion.WithResource("pods"), corev1.SchemeGroupVersion.WithKind("Pod"), namespace)
		if err != nil {
			return true, nil, err
		}
		jobs, err := kube.Tracker().List(batchv1.SchemeGroupVersion.WithResource("jobs"), batchv1.SchemeGroupVersion.WithKind("Job"), namespace)
		if err != nil {
			return true, nil, err
		}
		list := pods.(*corev1.PodList)
		for _, job := range jobs.(*batchv1.JobList).Items {
			if job.DeletionTimestamp != nil || (job.Spec.Suspend != nil && *job.Spec.Suspend) {
				continue
			}
			found := false
			for _, pod := range list.Items {
				if pod.Labels[JobLabel] == job.Name || pod.GenerateName == job.Name+"-" {
					found = true
				}
			}
			if !found {
				pod := corev1.Pod{ObjectMeta: *job.Spec.Template.ObjectMeta.DeepCopy(), Spec: *job.Spec.Template.Spec.DeepCopy()}
				pod.Name, pod.Namespace, pod.UID = job.Name+"-pod", job.Namespace, "pod-uid"
				pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr(true)}}
				list.Items = append(list.Items, pod)
			}
		}
		return true, list, nil
	})
	l := &Launcher{Kube: kube, Control: control, Boundary: boundary, Config: CloudJobConfig{Namespace: "cloud", RunnerImage: image, DockerImage: image, ControllerURL: "https://controller.example", CPUCeiling: 4, MemoryCeiling: 8 << 30}}
	installAdmission(kube, l.Config)
	return l, kube, boundary, control
}

func installAdmission(kube *fake.Clientset, cfg CloudJobConfig) {
	kube.PrependReactor("create", "selfsubjectreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.SelfSubjectReview{Status: authenticationv1.SelfSubjectReviewStatus{UserInfo: authenticationv1.UserInfo{Username: testLauncher}}}, nil
	})
	policies, bindings := AdmissionPolicies(AdmissionConfig{Namespace: cfg.Namespace, Launcher: testLauncher, RunnerImage: cfg.RunnerImage, DockerImage: cfg.DockerImage})
	for i := range policies {
		_ = kube.Tracker().Add(policies[i])
		_ = kube.Tracker().Add(bindings[i])
	}
}

// hack: the fake tracker ignores finalizers; the API server keeps a deleted Job until its last one is removed.
func retainFinalizedJobs(kube *fake.Clientset) {
	jobs := batchv1.SchemeGroupVersion.WithResource("jobs")
	tracker := kube.Tracker()
	kube.PrependReactor("delete", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		obj, err := tracker.Get(jobs, action.GetNamespace(), action.(ktesting.DeleteAction).GetName())
		if apierrors.IsNotFound(err) {
			return false, nil, nil
		}
		if err != nil {
			return true, nil, err
		}
		if len(obj.(*batchv1.Job).Finalizers) == 0 {
			return false, nil, nil
		}
		job := obj.(*batchv1.Job).DeepCopy()
		if job.DeletionTimestamp != nil {
			return true, nil, nil
		}
		job.DeletionTimestamp = &metav1.Time{Time: time.Now()}
		return true, nil, tracker.Update(jobs, job, job.Namespace)
	})
	kube.PrependReactor("patch", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		handled, obj, err := ktesting.ObjectReaction(tracker)(action)
		if job, ok := obj.(*batchv1.Job); err == nil && ok && job.DeletionTimestamp != nil && len(job.Finalizers) == 0 {
			return true, job, tracker.Delete(jobs, job.Namespace, job.Name)
		}
		return handled, obj, err
	})
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
	job, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: *job.Spec.Template.ObjectMeta.DeepCopy(), Spec: *job.Spec.Template.Spec.DeepCopy(), Status: corev1.PodStatus{Phase: corev1.PodRunning, InitContainerStatuses: []corev1.ContainerStatus{{Name: "docker", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}, ContainerStatuses: []corev1.ContainerStatus{{Name: "run-node", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}}}
	pod.Name, pod.Namespace = "pod", "cloud"
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: name, UID: job.UID, Controller: ptr(true)}}
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
	if err := l.Sync(t.Context()); err == nil || !strings.Contains(err.Error(), failure.Error()) || blocking(err) != nil {
		t.Fatalf("Job deletion failure = %v, want a non-blocking fault naming it", err)
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

func (c *controlFake) Prepare(ctx context.Context, _ []capability.LeaseRef) (time.Time, error) {
	return time.Now().Add(time.Minute), context.Cause(ctx)
}

type retryAfterControl struct {
	*controlFake
	claimed []time.Time
	stop    context.CancelFunc
}

func (c *retryAfterControl) Claim(ctx context.Context, _ string, _ []string) (*CloudJobAward, error) {
	c.claimed = append(c.claimed, time.Now())
	if len(c.claimed) > 1 {
		c.stop()
		return nil, nil
	}
	return nil, &apierr.Error{Code: apierr.RateLimited, Message: "poll later", Details: map[string]any{"retry_after_ms": float64(7000)}}
}

func TestLauncherWaitsOutAControllerRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, _, _, control := fixture()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		retrying := &retryAfterControl{controlFake: control, stop: cancel}
		l.Control, l.Poll = retrying, time.Second
		if err := l.Run(ctx); err != nil {
			t.Fatal(err)
		}
		if len(retrying.claimed) != 2 || retrying.claimed[1].Sub(retrying.claimed[0]) != 7*time.Second {
			t.Fatalf("claims at %v, want the second after the controller's 7s", retrying.claimed)
		}
	})
}

func TestForeignNamespaceReservationIsReported(t *testing.T) {
	l, kube, _, _ := fixture()
	var logs strings.Builder
	l.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "legacy", Annotations: map[string]string{reservationNamespaceAnnotation: "sparkwing-jobs", reservationAnnotation: "{}"}}}
	if _, err := kube.CoreV1().Nodes().Create(t.Context(), node, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "another job namespace") || !strings.Contains(logs.String(), "sparkwing-jobs") {
		t.Fatalf("legacy reservation went unreported: %s", logs.String())
	}
}
