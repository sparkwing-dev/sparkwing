package launcher

import (
	"errors"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestJobDeadlineFollowsFundedLeaseAcrossRenewalAndOutage(t *testing.T) {
	l, kube, _, control := fixture()
	first := time.Now().Add(time.Minute)
	control.award.LeaseExpiresAt = first
	if _, err := l.LaunchOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	name := jobName(control.award.Binding)
	jobs := kube.BatchV1().Jobs(l.Config.Namespace)
	job, err := jobs.Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if job.Annotations[deadlineAnnotation] != first.Format(time.RFC3339Nano) {
		t.Fatalf("Job deadline escaped funded bound: %v", job.Annotations)
	}
	if job.Spec.Template.Spec.TerminationGracePeriodSeconds == nil || *job.Spec.Template.Spec.TerminationGracePeriodSeconds != 0 {
		t.Fatal("pod can run beyond the bound during termination grace")
	}
	job.UID = "job-uid"
	if _, err := jobs.Update(t.Context(), job, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	pod := jobPod(job)
	pod.Name = name + "-pod"
	if _, err := kube.CoreV1().Pods(l.Config.Namespace).Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-10 * time.Second)
	job.Status.StartTime = &metav1.Time{Time: start}
	if _, err := jobs.UpdateStatus(t.Context(), job, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	second := time.Now().Add(2 * time.Minute)
	control.award.LeaseExpiresAt = second
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	renewed, err := jobs.Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Annotations[deadlineAnnotation] != second.Format(time.RFC3339Nano) || renewed.Spec.ActiveDeadlineSeconds == nil {
		t.Fatal("renewed lease did not reach Kubernetes")
	}
	if deadline := start.Add(time.Duration(*renewed.Spec.ActiveDeadlineSeconds) * time.Second); deadline.After(second) || second.Sub(deadline) >= time.Second {
		t.Fatalf("Kubernetes deadline %v differs from funded %v", deadline, second)
	}
	renewedPod, err := kube.CoreV1().Pods(l.Config.Namespace).Get(t.Context(), pod.Name, metav1.GetOptions{})
	if err != nil || renewedPod.Annotations[lifetimeAnnotation] != strconv.FormatInt(second.Unix(), 10) {
		t.Fatalf("renewal did not reach container watchdogs: %v %v", renewedPod, err)
	}
	control.fail = errors.New("controller offline")
	if err := l.Sync(t.Context()); err == nil {
		t.Fatal("controller outage hidden")
	}
	after, err := jobs.Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if *after.Spec.ActiveDeadlineSeconds != *renewed.Spec.ActiveDeadlineSeconds || after.Annotations[deadlineAnnotation] != renewed.Annotations[deadlineAnnotation] {
		t.Fatal("outage extended the last funded deadline")
	}
}

func TestJobDeadlineRequiresFundingAndRespectsExecutionAuthority(t *testing.T) {
	l, _, boundary, control := fixture()
	now := time.Now()
	award := *control.award
	award.LeaseExpiresAt = time.Time{}
	if _, err := BuildJob(t.Context(), now, l.Config, award, boundary); err == nil || boundary.reservations != 0 {
		t.Fatal("missing funded deadline reserved a node")
	}
	award.Binding.OwnerDeadline = now.Add(10 * time.Second)
	award.Binding.AttemptDeadline = time.Time{}
	award.Binding.ParentCapabilityExpiry = now.Add(time.Hour)
	award.LeaseExpiresAt = now.Add(time.Hour)
	resources, err := BuildJob(t.Context(), now, l.Config, award, boundary)
	if err != nil {
		t.Fatal(err)
	}
	if !resources.JobDeadline.Equal(award.Binding.OwnerDeadline) {
		t.Fatal("funded deadline escaped the execution authority")
	}
}

func TestJobDeadlineAccountsForDelayedStartTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, kube, _, control := fixture()
		launch := time.Now()
		bound := launch.Add(time.Minute)
		control.award.LeaseExpiresAt = bound
		if _, err := l.LaunchOne(t.Context()); err != nil {
			t.Fatal(err)
		}
		jobs := kube.BatchV1().Jobs(l.Config.Namespace)
		job, err := jobs.Get(t.Context(), jobName(control.award.Binding), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		start := launch.Add(45 * time.Second)
		// Kubernetes's relative backstop can lag; every entrypoint still receives the absolute bound.
		if got := job.Spec.Template.Annotations[lifetimeAnnotation]; got != strconv.FormatInt(bound.Unix(), 10) {
			t.Fatalf("delayed startup lost absolute bound: %s", got)
		}
		time.Sleep(45 * time.Second)
		job.Status.StartTime = &metav1.Time{Time: start}
		if _, err := jobs.UpdateStatus(t.Context(), job, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		control.fail = errors.New("controller offline")
		if err := l.Sync(t.Context()); err == nil {
			t.Fatal("controller outage hidden")
		}
		job, err = jobs.Get(t.Context(), job.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if deadline := start.Add(time.Duration(*job.Spec.ActiveDeadlineSeconds) * time.Second); !deadline.Equal(bound) {
			t.Fatalf("first StartTime sync gives deadline %v, want %v", deadline, bound)
		}
	})
}

func TestJobDeadlineDoesNotRoundAnElapsedDurationUp(t *testing.T) {
	now := time.Now()
	for _, duration := range []time.Duration{-time.Second, 0, time.Second / 2} {
		if got := jobDeadlineSeconds(now.Add(duration), now); got > 0 {
			t.Fatalf("%v rounded to %d seconds", duration, got)
		}
	}
}

func TestJobDeadlinePreservesShortPositiveDurations(t *testing.T) {
	now := time.Now()
	if got := jobDeadlineSeconds(now.Add(time.Second), now); got != 1 {
		t.Fatalf("one second of execution became %d seconds", got)
	}
}
