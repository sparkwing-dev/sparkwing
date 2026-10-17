package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ktesting "k8s.io/client-go/testing"
)

func TestProductionBoundaryRequiresMeasuredCanaries(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "enforced", true: "escape"}[failed], func(t *testing.T) {
			_, kube := boundaryFixture()
			_, err := kube.NetworkingV1().NetworkPolicies("cloud").Create(t.Context(), &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "deny", Namespace: "cloud", UID: "policy", ResourceVersion: "1"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}}}, metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			kube.PrependReactor("create", "networkpolicies", func(action ktesting.Action) (bool, runtime.Object, error) {
				policy := action.(ktesting.CreateAction).GetObject().(*networkingv1.NetworkPolicy)
				policy.UID = types.UID(policy.Name)
				return false, nil, nil
			})
			created := 0
			kube.PrependReactor("create", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
				pod := action.(ktesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
				if probe := pod.Spec.Containers[0].ReadinessProbe; probe != nil {
					if probe.TimeoutSeconds == 0 {
						probe.TimeoutSeconds = 1
					}
					if probe.PeriodSeconds == 0 {
						probe.PeriodSeconds = 10
					}
					if probe.SuccessThreshold == 0 {
						probe.SuccessThreshold = 1
					}
					if probe.FailureThreshold == 0 {
						probe.FailureThreshold = 3
					}
				}
				created++
				pod.UID = types.UID(pod.Name)
				pod.Status = corev1.PodStatus{PodIP: "10.1.2.3", HostIP: "10.0.0.1", Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}
				if pod.Spec.Containers[0].Name != "control" {
					pod.Status.Phase = corev1.PodSucceeded
					if failed && pod.Spec.Containers[0].Name == "measure" {
						pod.Status.Phase = corev1.PodFailed
					}
				}
				if err := kube.Tracker().Create(corev1.SchemeGroupVersion.WithResource("pods"), pod, pod.Namespace); err != nil {
					return true, nil, err
				}
				return true, pod, nil
			})
			production, err := NewProductionBoundary(kube, karpenterFixture(), CloudJobConfig{Namespace: "cloud", RunnerImage: "runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DockerImage: "docker@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ControllerURL: "https://controller.example", CPUCeiling: 1, MemoryCeiling: 1 << 30}, "https://kubernetes.default:443")
			if err != nil {
				t.Fatal(err)
			}
			reservation, evidence, err := production.Reserve(t.Context(), boundaryBinding("team"), "job")
			if failed && err == nil {
				t.Fatal("failed canary authorized work")
			}
			if !failed {
				if err != nil {
					t.Fatal(err)
				}
				if evidence.PolicyRevision == "" || !evidence.EscapeProbePassed || !evidence.DefaultDenyEgressEnforced || !evidence.RequiredDestinationsAllowed || !evidence.DaemonSetPortsInventoried {
					t.Fatalf("incomplete evidence: %+v", evidence)
				}
				if err = production.Release(t.Context(), reservation); err != nil {
					t.Fatal(err)
				}
			}
			pods, err := kube.CoreV1().Pods("cloud").List(t.Context(), metav1.ListOptions{})
			if err != nil || len(pods.Items) != 0 || created < 3 {
				t.Fatalf("canary lifecycle pods=%d created=%d error=%v", len(pods.Items), created, err)
			}
			node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if node.Annotations[reservationAnnotation] != "" {
				t.Fatal("finished canaries retained reservation")
			}
		})
	}
}

func FuzzProductionBoundaryCleanupReservationFence(f *testing.F) {
	f.Add("cloud", "reservation", true)
	f.Add("other", "reservation", false)
	f.Add("cloud", "other", false)
	f.Fuzz(func(t *testing.T, namespace, id string, replaced bool) {
		if namespace == "" || id == "" {
			t.Skip()
		}
		_, kube := boundaryFixture()
		held := CloudNodeReservation{NodeName: "node", NodeUID: "node-uid", Team: "team", JobName: "job", ReservationID: id}
		raw, err := json.Marshal(held)
		if err != nil {
			t.Fatal(err)
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "sw-boundary-canary", Namespace: namespace, UID: "original", Annotations: map[string]string{reservationAnnotation: string(raw)}}, Spec: corev1.PodSpec{NodeName: "node"}}
		if _, err = kube.CoreV1().Pods(namespace).Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		boundary, err := NewProductionBoundary(kube, karpenterFixture(), productionBoundaryConfig(), "https://kubernetes.default:443")
		if err != nil {
			t.Fatal(err)
		}
		own := namespace == "cloud" && id == "reservation"
		kube.PrependReactor("delete", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
			deletion := action.(ktesting.DeleteAction)
			options := deletion.GetDeleteOptions()
			if !own || options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != "original" {
				t.Fatal("cleanup exceeded its namespace/reservation/UID fence")
			}
			if replaced {
				return true, nil, errors.New("replacement instance")
			}
			return false, nil, nil
		})
		held.ReservationID = "reservation"
		err = boundary.StopProbes(t.Context(), held)
		if own && replaced && err == nil {
			t.Fatal("replacement deletion was accepted")
		}
		current, getErr := kube.CoreV1().Pods(namespace).Get(t.Context(), pod.Name, metav1.GetOptions{})
		if (!own || replaced) && (getErr != nil || current.UID != "original") {
			t.Fatal("foreign or replaced canary was deleted")
		}
		if own && !replaced && getErr == nil {
			t.Fatal("owned canary was retained")
		}
	})
}

func productionBoundaryConfig() CloudJobConfig {
	return CloudJobConfig{Namespace: "cloud", RunnerImage: "runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DockerImage: "docker@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ControllerURL: "https://controller.example", CPUCeiling: 1, MemoryCeiling: 1 << 30}
}

func FuzzProductionBoundaryRefusesChangedCanary(f *testing.F) {
	f.Add("/bin/true", true)
	f.Add("other-image", false)
	f.Fuzz(func(t *testing.T, replacement string, command bool) {
		if !command && replacement == productionBoundaryConfig().RunnerImage {
			t.Skip()
		}
		_, kube := boundaryFixture()
		policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "deny", Namespace: "cloud", UID: "policy"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}}}
		if _, err := kube.NetworkingV1().NetworkPolicies("cloud").Create(t.Context(), policy, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		kube.PrependReactor("create", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
			pod := action.(ktesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
			pod.UID = "canary"
			pod.Status.Phase = corev1.PodSucceeded
			if command {
				pod.Spec.Containers[0].Command = []string{replacement}
			} else {
				pod.Spec.Containers[0].Image = replacement
			}
			return true, pod, nil
		})
		boundary, err := NewProductionBoundary(kube, karpenterFixture(), productionBoundaryConfig(), "https://kubernetes.default:443")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = boundary.Reserve(t.Context(), boundaryBinding("team"), "job"); err == nil {
			t.Fatal("changed canary authorized execution")
		}
	})
}

func TestProductionBoundaryRequiresLiveIngressControl(t *testing.T) {
	_, kube := boundaryFixture()
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "deny", Namespace: "cloud", UID: "policy"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}}}
	if _, err := kube.NetworkingV1().NetworkPolicies("cloud").Create(t.Context(), policy, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	kube.PrependReactor("create", "networkpolicies", func(action ktesting.Action) (bool, runtime.Object, error) {
		policy := action.(ktesting.CreateAction).GetObject().(*networkingv1.NetworkPolicy)
		policy.UID = types.UID(policy.Name)
		return false, nil, nil
	})
	kube.PrependReactor("create", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		pod := action.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		pod.UID = types.UID(pod.Name)
		pod.Status = corev1.PodStatus{PodIP: "10.1.2.3", HostIP: "10.0.0.1", Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}
		switch pod.Spec.Containers[0].Name {
		case "witness":
			pod.Status.Phase = corev1.PodFailed
		case "measure":
			pod.Status.Phase = corev1.PodSucceeded
		}
		return false, nil, nil
	})
	boundary, err := NewProductionBoundary(kube, karpenterFixture(), productionBoundaryConfig(), "https://kubernetes.default:443")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = boundary.Reserve(t.Context(), boundaryBinding("team"), "job"); err == nil {
		t.Fatal("a failed positive ingress control authorized work")
	}
}

func FuzzProductionBoundaryRefusesConflictingDestinations(f *testing.F) {
	for source := uint8(0); source < 3; source++ {
		f.Add(source, uint16(10443))
	}
	f.Fuzz(func(t *testing.T, source uint8, rawPort uint16) {
		_, kube := boundaryFixture()
		port := int32(1024 + uint32(rawPort)%64512)
		config := productionBoundaryConfig()
		config.ControllerURL = fmt.Sprintf("https://10.0.0.1:%d", port)
		api := "https://api.example:443"
		switch source % 3 {
		case 0:
			api = config.ControllerURL
		case 1:
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "daemon", Namespace: "kube-system", OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Controller: ptr(true)}}}, Spec: corev1.PodSpec{NodeName: "node", Containers: []corev1.Container{{Name: "daemon", Ports: []corev1.ContainerPort{{ContainerPort: port, Protocol: corev1.ProtocolTCP}}}}}, Status: corev1.PodStatus{PodIP: "10.0.0.1"}}
			if _, err := kube.CoreV1().Pods("kube-system").Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		case 2:
			node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			node.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.1"}}
			node.Status.DaemonEndpoints.KubeletEndpoint.Port = port
			if _, err = kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "deny", Namespace: "cloud"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}}}
		if _, err := kube.NetworkingV1().NetworkPolicies("cloud").Create(t.Context(), policy, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		boundary, err := NewProductionBoundary(kube, karpenterFixture(), config, api)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = boundary.Reserve(t.Context(), boundaryBinding("team"), "job")
		if err == nil || !strings.Contains(err.Error(), "conflicting required and forbidden Cloud destination") {
			t.Fatalf("contradictory endpoint class %d accepted or obscured: %v", source%3, err)
		}
	})
}
