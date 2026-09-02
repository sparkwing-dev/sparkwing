package pool

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCheckoutKeepsAHostileJobIDOnOneLogLine(t *testing.T) {
	hostile := "job-1\npool: checked out PVC sparkwing-cache-pool-9 for job forged"
	logged := capturePoolLog(t)

	client := fake.NewSimpleClientset(&corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "sparkwing-cache-pool-0",
			Namespace:   "builds",
			Labels:      map[string]string{PoolLabelKey: PoolLabelValue},
			Annotations: map[string]string{AnnPoolState: StateClean},
		},
	})
	p := NewPool(client, "builds", 1, "")

	name, err := p.Checkout(context.Background(), hostile)
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if name != "sparkwing-cache-pool-0" {
		t.Fatalf("Checkout = %q, want sparkwing-cache-pool-0", name)
	}

	out := strings.TrimSuffix(logged.String(), "\n")
	if strings.Contains(out, "\n") {
		t.Fatalf("checkout log = %q, want the job id escaped onto one line", out)
	}
	if !strings.Contains(out, `\npool: checked out`) {
		t.Fatalf("checkout log = %q, want the newline rendered as an escape", out)
	}
}

func TestReclaimKeepsAHostileOwnerOnOneLogLine(t *testing.T) {
	hostile := "job-1\npool: reclaiming abandoned PVC forged"
	logged := capturePoolLog(t)

	client := fake.NewSimpleClientset(&corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sparkwing-cache-pool-0",
			Namespace: "builds",
			Labels:    map[string]string{PoolLabelKey: PoolLabelValue},
			Annotations: map[string]string{
				AnnPoolState:    StateInUse,
				AnnCheckedOutBy: hostile,
				AnnCheckedOutAt: "2020-01-01T00:00:00Z",
			},
		},
	})
	p := NewPool(client, "builds", 1, "")

	if err := p.Reconcile(context.Background(), time.Minute, time.Minute); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	out := logged.String()
	if !strings.Contains(out, `\npool: reclaiming abandoned PVC forged`) {
		t.Fatalf("reclaim log = %q, want the owner newline rendered as an escape", out)
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(out, "\n"), "\n") {
		if strings.HasPrefix(line, "pool: reclaiming abandoned PVC forged") {
			t.Fatalf("reclaim log = %q, want no forged line", out)
		}
	}
}

func capturePoolLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &logged
}
