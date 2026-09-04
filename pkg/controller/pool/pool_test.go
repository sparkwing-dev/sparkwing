package pool

import (
	"bytes"
	"context"
	"log"
	"os"
	"slices"
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

func TestReconcileFillsTheLowestFreeMemberIndex(t *testing.T) {
	logged := capturePoolLog(t)

	client := fake.NewSimpleClientset(
		poolPVC("sparkwing-cache-pool-1", "1"),
		poolPVC("sparkwing-cache-pool-2", "2"),
	)
	p := NewPool(client, "builds", 3, "")

	for range 3 {
		if err := p.Reconcile(context.Background(), time.Minute, time.Minute); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}

	pvcs, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var names []string
	for _, pvc := range pvcs {
		names = append(names, pvc.Name)
	}
	want := []string{"sparkwing-cache-pool-0", "sparkwing-cache-pool-1", "sparkwing-cache-pool-2"}
	if !slices.Equal(names, want) {
		t.Fatalf("pool after 3 reconciles = %v, want %v", names, want)
	}

	out := logged.String()
	if strings.Count(out, "pool: created sparkwing-cache-pool-0") != 1 {
		t.Fatalf("pool log = %q, want exactly one creation of member 0", out)
	}
	if strings.Contains(out, "pool: created sparkwing-cache-pool-2") {
		t.Fatalf("pool log = %q, want no creation line for the member that already existed", out)
	}
}

func TestReconcileReportsAPVCThatAlreadyExisted(t *testing.T) {
	logged := capturePoolLog(t)

	unlabelled := poolPVC("sparkwing-cache-pool-0", "0")
	unlabelled.Labels = nil
	client := fake.NewSimpleClientset(unlabelled)
	p := NewPool(client, "builds", 1, "")

	if err := p.Reconcile(context.Background(), time.Minute, time.Minute); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	out := logged.String()
	if strings.Contains(out, "pool: created sparkwing-cache-pool-0") {
		t.Fatalf("pool log = %q, want no created line for a PVC that already existed", out)
	}
	if !strings.Contains(out, "sparkwing-cache-pool-0 already exists") {
		t.Fatalf("pool log = %q, want the already-exists line", out)
	}
}

func poolPVC(name, member string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "builds",
			Labels:    map[string]string{PoolLabelKey: PoolLabelValue},
			Annotations: map[string]string{
				AnnPoolState:  StateClean,
				AnnPoolMember: member,
			},
		},
	}
}

func TestReconcileReservesBothIndexesOfADisagreeingPVC(t *testing.T) {
	client := fake.NewSimpleClientset(poolPVC("sparkwing-cache-pool-0", "2"))
	p := NewPool(client, "builds", 3, "")

	for range 3 {
		if err := p.Reconcile(context.Background(), time.Minute, time.Minute); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}

	pvcs, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var names []string
	for _, pvc := range pvcs {
		names = append(names, pvc.Name)
	}
	want := []string{"sparkwing-cache-pool-0", "sparkwing-cache-pool-1", "sparkwing-cache-pool-3"}
	if !slices.Equal(names, want) {
		t.Fatalf("pool after 3 reconciles = %v, want %v", names, want)
	}
}
