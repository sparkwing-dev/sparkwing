package launcher

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ktesting "k8s.io/client-go/testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func FuzzCapacityIdentityRefusalIsLocal(f *testing.F) {
	for kind := uint8(0); kind < 8; kind++ {
		f.Add("forged", kind, false)
	}
	f.Add("forged", uint8(0), true)
	f.Add("\xe8", uint8(0), true)
	f.Fuzz(func(t *testing.T, holder string, kind uint8, replaced bool) {
		holder = "forged-" + hex.EncodeToString([]byte(holder))
		l, kube, boundary, control := fixture()
		first := *control.award
		second := first
		second.Binding.UnitID, second.Binding.NodeID = "unit-2", "job-2"
		for i, award := range []CloudJobAward{first, second} {
			pod, err := capacityPod(l.Config, demandFor(award))
			if err != nil {
				t.Fatal(err)
			}
			pod.UID = types.UID(fmt.Sprintf("request-%d", i))
			if i == 0 {
				d := demandFor(award)
				d.Binding.Holder = holder
				raw, err := json.Marshal(d)
				if err != nil {
					t.Fatal(err)
				}
				pod.Annotations[demandAnnotation] = string(raw)
			}
			if _, err := kube.CoreV1().Pods("cloud").Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		deletes := 0
		kube.PrependReactor("delete", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
			deletion := action.(ktesting.DeleteAction)
			pre := deletion.GetDeleteOptions().Preconditions
			if deletion.GetName() != capacityName(first.Binding) || pre == nil || pre.UID == nil || *pre.UID != "request-0" {
				t.Fatal("identity refusal exceeded its exact Pod UID")
			}
			deletes++
			if replaced {
				pod, err := kube.Tracker().Get(corev1.SchemeGroupVersion.WithResource("pods"), "cloud", deletion.GetName())
				if err != nil {
					t.Fatal(err)
				}
				replacement := pod.(*corev1.Pod).DeepCopy()
				replacement.UID = "replacement"
				if err := kube.Tracker().Update(corev1.SchemeGroupVersion.WithResource("pods"), replacement, "cloud"); err != nil {
					t.Fatal(err)
				}
				return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, deletion.GetName(), errors.New("Pod replaced"))
			}
			return false, nil, nil
		})
		failures := []error{
			apierr.Authorization(apierr.ReasonBinding, "unknown award fence"),
			&apierr.Error{Code: apierr.Forbidden, Message: "unknown award fence", Details: map[string]any{"reason": "binding"}},
			&apierr.Error{Code: apierr.LockHeld, Message: "claim fence lost"},
			&apierr.Error{Code: apierr.Unavailable, Message: "try again"},
			&apierr.Error{Code: apierr.RateLimited, Message: "poll later"},
			&apierr.Error{Code: apierr.Unauthenticated, Message: "service credential expired"},
			apierr.Authorization(apierr.ReasonMissingGrant, "missing scope"),
			errors.New("transport failure"),
		}
		kind %= uint8(len(failures))
		failure, retry := failures[kind], kind >= 3
		authenticated, refusals := 0, 0
		control.award = nil
		restarted := &Launcher{Kube: kube, Config: l.Config, Boundary: boundary, Control: publicationControl{CloudControl: control, sync: func(obs []CloudJobObservation) ([]CloudJobState, error) {
			for _, o := range obs {
				if o.Binding.UnitID == first.Binding.UnitID {
					refusals++
					return nil, failure
				}
			}
			authenticated++
			return awardedStates(second, func(CloudJobBinding) bool { return false })(obs)
		}}}
		err := restarted.Sync(t.Context())
		if retry {
			if !errors.Is(err, failure) || deletes != 0 {
				t.Fatalf("transient failure must retain and retry: %v deletes=%d", err, deletes)
			}
			if err := restarted.Sync(t.Context()); !errors.Is(err, failure) || refusals != 2 {
				t.Fatalf("transient identity check was not retried: %v calls=%d", err, refusals)
			}
		} else {
			if err != nil || deletes != 1 {
				t.Fatalf("identity refusal poisoned reconciliation: %v deletes=%d", err, deletes)
			}
			if _, err := restarted.LaunchOne(t.Context()); err != nil || control.claims != 1 {
				t.Fatalf("identity refusal blocked new claims: %v claims=%d", err, control.claims)
			}
		}
		if !retry && replaced {
			pod, err := kube.CoreV1().Pods("cloud").Get(t.Context(), capacityName(first.Binding), metav1.GetOptions{})
			if err != nil || pod.UID != "replacement" {
				t.Fatalf("refused identity deleted a replacement Pod: %v", err)
			}
		}
		if authenticated == 0 {
			t.Fatal("one request prevented authenticating the other request")
		}
		if _, err := kube.CoreV1().Pods("cloud").Get(t.Context(), capacityName(second.Binding), metav1.GetOptions{}); err != nil {
			t.Fatalf("other award lost capacity: %v", err)
		}
	})
}

type claimSignalControl struct {
	CloudControl
	claimed chan struct{}
}

func (c claimSignalControl) Claim(ctx context.Context, _ string, _ []string) (*CloudJobAward, error) {
	close(c.claimed)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRunClaimsAfterRefusingRestartedCapacityIdentity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, kube, boundary, control := fixture()
		if err := l.requestCapacity(t.Context(), *control.award); err != nil {
			t.Fatal(err)
		}
		pod := capacityPods(t, kube)[0]
		d := demandFor(*control.award)
		d.Binding.Holder = "forged-after-persistence"
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		pod.Annotations[demandAnnotation] = string(raw)
		if _, err := kube.CoreV1().Pods("cloud").Update(t.Context(), &pod, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		claimed := make(chan struct{})
		restarted := &Launcher{Kube: kube, Config: l.Config, Boundary: boundary, Control: claimSignalControl{CloudControl: publicationControl{CloudControl: control, sync: func([]CloudJobObservation) ([]CloudJobState, error) {
			return nil, apierr.Authorization(apierr.ReasonBinding, "forged holder")
		}}, claimed: claimed}}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- restarted.Run(ctx) }()
		synctest.Wait()
		select {
		case <-claimed:
		default:
			t.Error("a forged persisted holder prevented Run from reaching LaunchOne")
		}
		if len(capacityPods(t, kube)) != 0 {
			t.Error("a forged persisted holder retained capacity")
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
