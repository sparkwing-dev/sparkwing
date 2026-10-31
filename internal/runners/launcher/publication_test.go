package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
)

type publicationControl struct {
	CloudControl
	sync func([]CloudJobObservation) ([]CloudJobState, error)
}

func (c publicationControl) Sync(_ context.Context, bindings []CloudJobObservation) ([]CloudJobState, error) {
	return c.sync(bindings)
}

func TestSyncSuspendedJobRequiresRetirementEvidence(t *testing.T) {
	failure := errors.New("controller unavailable")
	for _, test := range []struct {
		name    string
		remove  bool
		failure error
		expired bool
		keep    bool
	}{
		{name: "live", keep: true},
		{name: "cancelled", remove: true},
		{name: "unreachable", failure: failure, keep: true},
		{name: "expired", failure: failure, expired: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			l, kube, boundary, control := fixture()
			if _, err := l.LaunchOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			jobsBefore, err := kube.BatchV1().Jobs(l.Config.Namespace).List(t.Context(), metav1.ListOptions{})
			if err != nil || len(jobsBefore.Items) != 1 {
				t.Fatalf("publication: %v", err)
			}
			job := jobsBefore.Items[0].DeepCopy()
			job.Spec.Suspend = ptr(true)
			if test.expired {
				job.Annotations[deadlineAnnotation] = time.Now().Add(-time.Second).Format(time.RFC3339Nano)
			}
			if _, err := kube.BatchV1().Jobs(l.Config.Namespace).Update(t.Context(), job, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			control.remove, control.fail = test.remove, test.failure
			err = l.Sync(t.Context())
			if test.failure != nil && !test.expired && !errors.Is(err, failure) {
				t.Fatalf("controller failure lost: %v", err)
			}
			if (test.failure == nil || test.expired) && err != nil {
				t.Fatal(err)
			}
			jobs, err := kube.BatchV1().Jobs(l.Config.Namespace).List(t.Context(), metav1.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			secrets, err := kube.CoreV1().Secrets(l.Config.Namespace).List(t.Context(), metav1.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if (len(jobs.Items) == 1) != test.keep || (len(secrets.Items) == 1) != test.keep || (boundary.releases == 0) != test.keep {
				t.Fatalf("jobs=%d bootstraps=%d releases=%d keep=%t", len(jobs.Items), len(secrets.Items), boundary.releases, test.keep)
			}
		})
	}
}

func TestSyncSuspendedJobRefusesMismatchedAuthority(t *testing.T) {
	l, kube, boundary, control := fixture()
	r, err := BuildJob(t.Context(), time.Now(), l.Config, *control.award, boundary)
	if err != nil {
		t.Fatal(err)
	}
	r.Job.Spec.Suspend = ptr(true)
	if _, err := kube.BatchV1().Jobs(l.Config.Namespace).Create(t.Context(), r.Job, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	l.Control = publicationControl{CloudControl: control, sync: func(bindings []CloudJobObservation) ([]CloudJobState, error) {
		binding := bindings[0].Binding
		binding.Generation++
		return []CloudJobState{{Binding: binding, LeaseExpiresAt: time.Now().Add(time.Hour), Delete: true}}, nil
	}}
	if err := l.Sync(t.Context()); err == nil {
		t.Fatal("mismatched retirement decision accepted")
	}
	if boundary.releases != 0 {
		t.Fatal("mismatched decision released the node")
	}
}

func TestLauncherChecksAuthorityAfterBootstrapPublication(t *testing.T) {
	failure := errors.New("controller unavailable")
	for _, test := range []struct {
		name  string
		state func(CloudJobBinding) ([]CloudJobState, error)
	}{
		{name: "cancelled", state: func(b CloudJobBinding) ([]CloudJobState, error) {
			return []CloudJobState{{Binding: b, LeaseExpiresAt: time.Now().Add(time.Hour), Delete: true}}, nil
		}},
		{name: "unreachable", state: func(CloudJobBinding) ([]CloudJobState, error) {
			return nil, failure
		}},
		{name: "missing", state: func(CloudJobBinding) ([]CloudJobState, error) {
			return nil, nil
		}},
		{name: "mismatched", state: func(b CloudJobBinding) ([]CloudJobState, error) {
			b.Generation++
			return []CloudJobState{{Binding: b, LeaseExpiresAt: time.Now().Add(time.Hour)}}, nil
		}},
		{name: "owner_deadline", state: func(b CloudJobBinding) ([]CloudJobState, error) {
			b.OwnerDeadline = b.OwnerDeadline.Add(time.Nanosecond)
			return []CloudJobState{{Binding: b, LeaseExpiresAt: time.Now().Add(time.Hour)}}, nil
		}},
		{name: "attempt_deadline", state: func(b CloudJobBinding) ([]CloudJobState, error) {
			b.AttemptDeadline = b.AttemptDeadline.Add(time.Nanosecond)
			return []CloudJobState{{Binding: b, LeaseExpiresAt: time.Now().Add(time.Hour)}}, nil
		}},
		{name: "parent_expiry", state: func(b CloudJobBinding) ([]CloudJobState, error) {
			b.ParentCapabilityExpiry = b.ParentCapabilityExpiry.Add(time.Nanosecond)
			return []CloudJobState{{Binding: b, LeaseExpiresAt: time.Now().Add(time.Hour)}}, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			l, kube, boundary, control := fixture()
			checked := false
			l.Control = publicationControl{CloudControl: control, sync: func(bindings []CloudJobObservation) ([]CloudJobState, error) {
				checked = true
				published := false
				for _, action := range kube.Actions() {
					if action.GetVerb() == "create" && action.GetResource().Resource == "secrets" {
						published = true
					}
				}
				if !published {
					t.Fatal("authority check preceded bootstrap publication")
				}
				return test.state(bindings[0].Binding)
			}}
			claimed, err := l.LaunchOne(t.Context())
			if !claimed || err == nil || !checked {
				t.Fatalf("unconfirmed award activated: claimed=%t err=%v checked=%t", claimed, err, checked)
			}
			if test.name == "unreachable" && !errors.Is(err, failure) {
				t.Fatalf("controller failure lost: %v", err)
			}
			for _, action := range kube.Actions() {
				if action.GetVerb() == "patch" && action.GetResource().Resource == "jobs" {
					var patch struct {
						Spec struct{ Suspend *bool }
					}
					if err := json.Unmarshal(action.(ktesting.PatchAction).GetPatch(), &patch); err != nil {
						t.Fatal(err)
					}
					if patch.Spec.Suspend != nil && !*patch.Spec.Suspend {
						t.Fatal("unconfirmed award became runnable")
					}
				}
			}
			secrets, err := kube.CoreV1().Secrets(l.Config.Namespace).List(t.Context(), metav1.ListOptions{})
			if err != nil || len(secrets.Items) != 0 || boundary.releases != 1 {
				t.Fatalf("failed publication leaked: secrets=%+v releases=%d err=%v", secrets, boundary.releases, err)
			}
		})
	}
}

func TestLauncherAcceptsRoundTrippedBindingTimes(t *testing.T) {
	l, _, boundary, control := fixture()
	now := time.Now()
	control.award.Binding.OwnerDeadline = now.Add(time.Hour)
	control.award.Binding.AttemptDeadline = now.Add(time.Minute)
	control.award.Binding.ParentCapabilityExpiry = now.Add(2 * time.Hour)
	l.Control = publicationControl{CloudControl: control, sync: func(bindings []CloudJobObservation) ([]CloudJobState, error) {
		body, err := json.Marshal(bindings[0].Binding)
		if err != nil {
			t.Fatal(err)
		}
		var binding CloudJobBinding
		if err := json.Unmarshal(body, &binding); err != nil {
			t.Fatal(err)
		}
		binding.OwnerDeadline = binding.OwnerDeadline.In(time.FixedZone("controller", 3600))
		return []CloudJobState{{Binding: binding, LeaseExpiresAt: time.Now().Add(time.Hour)}}, nil
	}}
	if claimed, err := l.LaunchOne(t.Context()); !claimed || err != nil {
		t.Fatalf("equivalent binding refused after transport: claimed=%t err=%v", claimed, err)
	}
	if boundary.releases != 0 {
		t.Fatal("equivalent binding lost its node reservation")
	}
	if err := l.Sync(t.Context()); err != nil {
		t.Fatalf("equivalent binding refused during reconciliation: %v", err)
	}
}

func TestPublicationRechecksDeadlineAfterConcurrentRenewal(t *testing.T) {
	for _, alterActivation := range []bool{false, true} {
		t.Run(fmt.Sprintf("alter_activation=%t", alterActivation), func(t *testing.T) {
			l, kube, _, control := fixture()
			funded := time.Now().Add(2 * time.Minute)
			control.award.LeaseExpiresAt = funded
			retained, activated := false, false
			kube.PrependReactor("patch", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
				patching := action.(ktesting.PatchAction)
				var patch batchv1.Job
				if err := json.Unmarshal(patching.GetPatch(), &patch); err != nil {
					t.Fatal(err)
				}
				if patch.Spec.Suspend != nil && !*patch.Spec.Suspend {
					activated = true
				}
				if patch.Annotations[bootstrapAnnotation] == "" && !alterActivation {
					return false, nil, nil
				}
				gvr := batchv1.SchemeGroupVersion.WithResource("jobs")
				object, err := kube.Tracker().Get(gvr, action.GetNamespace(), patching.GetName())
				if err != nil {
					t.Fatal(err)
				}
				job := object.(*batchv1.Job).DeepCopy()
				if patch.Annotations[bootstrapAnnotation] != "" {
					retained = true
					job.Annotations[bootstrapAnnotation] = patch.Annotations[bootstrapAnnotation]
				} else if patch.Spec.Suspend != nil {
					job.Spec.Suspend = patch.Spec.Suspend
					job.Annotations[launchAnnotation] = patch.Annotations[launchAnnotation]
				} else {
					return false, nil, nil
				}
				job.Annotations[deadlineAnnotation] = funded.Add(time.Hour).Format(time.RFC3339Nano)
				job.Spec.ActiveDeadlineSeconds = ptr(int64(3600))
				if err := kube.Tracker().Update(gvr, job, action.GetNamespace()); err != nil {
					t.Fatal(err)
				}
				return true, job, nil
			})
			claimed, err := l.LaunchOne(t.Context())
			if !claimed || !retained || !activated || (err != nil) != alterActivation {
				t.Fatalf("publication: claimed=%t retained=%t activated=%t err=%v", claimed, retained, activated, err)
			}
			if alterActivation {
				return
			}
			job, err := kube.BatchV1().Jobs(l.Config.Namespace).Get(t.Context(), jobName(control.award.Binding), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			start, err := time.Parse(time.RFC3339Nano, job.Annotations[launchAnnotation])
			if err != nil {
				t.Fatal(err)
			}
			if job.Spec.Suspend == nil || *job.Spec.Suspend || job.Annotations[deadlineAnnotation] != funded.Format(time.RFC3339Nano) || job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != jobDeadlineSeconds(funded, start) {
				t.Fatalf("activation did not restore funded deadline: %+v", job)
			}
		})
	}
}
