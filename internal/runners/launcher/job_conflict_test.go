package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// writingJobController models the API server's resourceVersion check and the
// Kubernetes Job controller, which writes Job status between the launcher's
// own writes: on Secret creation and on every Job delete or patch.
func writingJobController(t *testing.T, kube *fake.Clientset) {
	t.Helper()
	gvr := batchv1.SchemeGroupVersion.WithResource("jobs")
	tracker := kube.Tracker()
	bump := func(namespace, name string) {
		obj, err := tracker.Get(gvr, namespace, name)
		if err != nil {
			return
		}
		job := obj.(*batchv1.Job).DeepCopy()
		rv, _ := strconv.Atoi(job.ResourceVersion)
		job.ResourceVersion = strconv.Itoa(rv + 1)
		if err := tracker.Update(gvr, job, namespace); err != nil {
			t.Error(err)
		}
	}
	bumpAll := func(namespace string) {
		list, err := tracker.List(gvr, batchv1.SchemeGroupVersion.WithKind("Job"), namespace)
		if err != nil {
			return
		}
		for _, job := range list.(*batchv1.JobList).Items {
			bump(job.Namespace, job.Name)
		}
	}
	bumpAll(metav1.NamespaceAll)
	kube.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		bumpAll(action.GetNamespace())
		return false, nil, nil
	})
	kube.PrependReactor("delete", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		bump(action.GetNamespace(), action.(ktesting.DeleteAction).GetName())
		return false, nil, nil
	})
	kube.PrependReactor("patch", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		patch := action.(ktesting.PatchAction)
		var body struct {
			Metadata struct {
				ResourceVersion string `json:"resourceVersion"`
			} `json:"metadata"`
		}
		_ = json.Unmarshal(patch.GetPatch(), &body)
		obj, err := tracker.Get(gvr, patch.GetNamespace(), patch.GetName())
		if err == nil && body.Metadata.ResourceVersion != "" && body.Metadata.ResourceVersion != obj.(*batchv1.Job).ResourceVersion {
			return true, nil, apierrors.NewConflict(gvr.GroupResource(), patch.GetName(), errors.New("the object has been modified"))
		}
		bump(patch.GetNamespace(), patch.GetName())
		return false, nil, nil
	})
	kube.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		action.(ktesting.CreateAction).GetObject().(*batchv1.Job).ResourceVersion = "1"
		return false, nil, nil
	})
}

// The Job controller writes status between create and the launcher's
// patches; activation and teardown must survive that conflict.
func TestLaunchSurvivesJobControllerWrites(t *testing.T) {
	l, kube, boundary, control := fixture()
	writingJobController(t, kube)
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	name := jobName(control.award.Binding)
	job, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if job.Spec.Suspend == nil || *job.Spec.Suspend || job.Annotations[bootstrapAnnotation] == "" {
		t.Fatalf("Job left suspended=%v bootstrap=%q", job.Spec.Suspend, job.Annotations[bootstrapAnnotation])
	}
	teardownSurvivesJobControllerWrites(t, l, kube, boundary, control)
}

func TestTeardownSurvivesJobControllerWrites(t *testing.T) {
	l, kube, boundary, control := fixture()
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	writingJobController(t, kube)
	teardownSurvivesJobControllerWrites(t, l, kube, boundary, control)
}

func teardownSurvivesJobControllerWrites(t *testing.T, l *Launcher, kube *fake.Clientset, boundary *boundaryFake, control *controlFake) {
	t.Helper()
	name := jobName(control.award.Binding)
	control.remove = true
	for range 2 {
		if err := l.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := kube.BatchV1().Jobs("cloud").Get(t.Context(), name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("terminating Job kept its cleanup finalizer: %v", err)
	}
	if boundary.releases == 0 {
		t.Fatal("node was not released")
	}
}

// A Job whose teardown keeps failing must not stop claims for other work.
func TestFailingTeardownKeepsClaiming(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, kube, _, control := fixture()
		if _, err := l.LaunchOne(t.Context()); err != nil {
			t.Fatal(err)
		}
		kube.PrependReactor("patch", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewInternalError(errors.New("finalizer patch unavailable"))
		})
		control.remove = true
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		claims := &countingClaims{controlFake: control, stop: cancel}
		l.Control, l.Poll = claims, time.Second
		if err := l.Run(ctx); err != nil {
			t.Fatal(err)
		}
		if claims.n < 2 {
			t.Fatalf("claims = %d after a failing teardown, want claiming to continue", claims.n)
		}
	})
}

type countingClaims struct {
	*controlFake
	n    int
	stop context.CancelFunc
}

func (c *countingClaims) Claim(context.Context, string, []string) (*CloudJobAward, error) {
	if c.n++; c.n >= 2 {
		c.stop()
	}
	return nil, nil
}
