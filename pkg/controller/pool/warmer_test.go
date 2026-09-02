package pool

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestWarmPVCCancellationStopsAndDeletesTheWarmer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		cancel()
		return false, nil, nil
	})

	result := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		result <- WarmPVC(ctx, client, "builds", "sparkwing-cache-pool-1", nil)
	}()
	t.Cleanup(func() {
		cancel()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-finished:
		case <-timer.C:
			t.Error("WarmPVC did not stop during cleanup")
		}
	})

	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WarmPVC error = %v, want context.Canceled", err)
		}
	case <-timer.C:
		t.Fatal("WarmPVC did not stop after context cancellation")
	}

	pods, err := client.CoreV1().Pods("builds").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 0 {
		t.Fatalf("warmer pods after cancellation = %d, want 0", len(pods.Items))
	}
}

func TestWarmPVCRunsUnderAnUnprivilegedServiceAccountWithNoToken(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := fake.NewSimpleClientset()
	created := make(chan *corev1.Pod, 1)
	client.PrependReactor("create", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		created <- action.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		cancel()
		return false, nil, nil
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = WarmPVC(ctx, client, "builds", "sparkwing-cache-pool-1", nil)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case pod := <-created:
		if pod.Spec.ServiceAccountName != WarmerServiceAccountName {
			t.Errorf("warmer service account = %q, want %q", pod.Spec.ServiceAccountName, WarmerServiceAccountName)
		}
		if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
			t.Errorf("automountServiceAccountToken = %v, want false", pod.Spec.AutomountServiceAccountToken)
		}
	case <-timer.C:
		t.Fatal("WarmPVC never created a warmer pod")
	}
}
