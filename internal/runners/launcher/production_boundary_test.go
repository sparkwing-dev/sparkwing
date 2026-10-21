package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ktesting "k8s.io/client-go/testing"
)

func TestProductionBoundaryRequiresMeasuredCanaries(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "enforced", true: "escape"}[failed], func(t *testing.T) {
			_, kube := boundaryFixture()
			_, err := kube.NetworkingV1().NetworkPolicies("cloud").Create(t.Context(), &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "deny", Namespace: "cloud", UID: "policy", ResourceVersion: "1"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}}}, metav1.CreateOptions{})
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
				nodeTopology(pod)
				pod.UID = types.UID(pod.Name)
				pod.Status = corev1.PodStatus{PodIP: "10.1.2.3", HostIP: "10.0.0.1", Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}
				if name := pod.Spec.Containers[0].Name; name != "control" && name != "peer" {
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

func nodeTopology(p *corev1.Pod) {
	if p.Labels == nil {
		p.Labels = map[string]string{}
	}
	p.Labels[corev1.LabelTopologyRegion], p.Labels[corev1.LabelTopologyZone] = "us-west-2", "us-west-2a"
}

func TestValidateCanaryAllowsOnlyNodeTopologyLabels(t *testing.T) {
	f := newAdmissionFixture(t)
	expected := f.probes.canary(f.probeCtx, f.resources.Reservation, "nonce")
	admitted := expected.DeepCopy()
	admitted.UID = "canary"
	nodeTopology(admitted)
	if err := f.probes.validateCanary(expected, admitted); err != nil {
		t.Fatalf("API server topology labels refused: %v", err)
	}
	for _, key := range []string{corev1.LabelHostname, "topology.k8s.io/rack", TeamLabel} {
		changed := admitted.DeepCopy()
		changed.Labels[key] = "other"
		if err := f.probes.validateCanary(expected, changed); err == nil {
			t.Fatalf("label %s accepted", key)
		}
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
		policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "deny", Namespace: "cloud", UID: "policy"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}}}
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
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "deny", Namespace: "cloud", UID: "policy"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}}}
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
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "daemon", Namespace: "kube-system", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Controller: ptr(true)}}}, Spec: corev1.PodSpec{NodeName: "node", Containers: []corev1.Container{{Name: "daemon", Ports: []corev1.ContainerPort{{ContainerPort: port, Protocol: corev1.ProtocolTCP}}}}}, Status: corev1.PodStatus{PodIP: "10.0.0.1"}}
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
		policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "deny", Namespace: "cloud"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}}}
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

func TestProductionBoundaryMeasuresIngressAndDeniedEndpoints(t *testing.T) {
	for _, ingress := range []bool{true, false} {
		_, kube := boundaryFixture()
		directions := []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}
		if ingress {
			directions = append(directions, networkingv1.PolicyTypeIngress)
		}
		policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "deny", Namespace: "cloud", UID: "policy"}, Spec: networkingv1.NetworkPolicySpec{PolicyTypes: directions}}
		port := int32(443)
		slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default", Labels: map[string]string{discoveryv1.LabelServiceName: "kubernetes"}}, AddressType: discoveryv1.AddressTypeIPv4, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"172.16.0.10"}}}, Ports: []discoveryv1.EndpointPort{{Port: &port}}}
		for _, object := range []runtime.Object{policy, slice} {
			if err := kube.Tracker().Add(object); err != nil {
				t.Fatal(err)
			}
		}
		var control *networkingv1.NetworkPolicy
		kube.PrependReactor("create", "networkpolicies", func(action ktesting.Action) (bool, runtime.Object, error) {
			control = action.(ktesting.CreateAction).GetObject().(*networkingv1.NetworkPolicy)
			control.UID = types.UID(control.Name)
			return false, nil, nil
		})
		var measured, witnessed []string
		var peer *corev1.Pod
		kube.PrependReactor("create", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
			pod := action.(ktesting.CreateAction).GetObject().(*corev1.Pod)
			pod.UID = types.UID(pod.Name)
			pod.Status = corev1.PodStatus{PodIP: "10.1.2.3", Phase: corev1.PodSucceeded}
			switch pod.Spec.Containers[0].Name {
			case "peer":
				peer = pod
				pod.Status.PodIP = "10.1.2.4"
				fallthrough
			case "control":
				pod.Status.Phase, pod.Status.Conditions = corev1.PodRunning, []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
			case "witness":
				witnessed = pod.Spec.Containers[0].Args
			case "measure":
				measured = pod.Spec.Containers[0].Args
			}
			return false, nil, nil
		})
		boundary, err := NewProductionBoundary(kube, karpenterFixture(), productionBoundaryConfig(), "https://kubernetes.default:443")
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = boundary.Reserve(t.Context(), boundaryBinding("team"), "job")
		if !ingress {
			if err == nil || !strings.Contains(err.Error(), "ingress") {
				t.Fatalf("Jobs without default-deny ingress authorized work: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(witnessed, " ") != "10.1.2.3 10.1.2.4" {
			t.Fatalf("witness must reach the control and be refused by the peer: %v", witnessed)
		}
		if peer == nil || peer.Spec.Containers[0].ReadinessProbe == nil || peer.Spec.Containers[0].ReadinessProbe.Exec == nil {
			t.Fatal("peer listener is not readiness-confirmed")
		}
		peerLabels := labels.Set(peer.Labels)
		if allow, _ := metav1.LabelSelectorAsSelector(&control.Spec.PodSelector); allow.Matches(peerLabels) || peer.Labels["app.kubernetes.io/managed-by"] != managedByLauncher {
			t.Fatalf("peer is not governed by Job ingress policy alone: %v", peer.Labels)
		}
		reachesPeer := false
		for _, to := range control.Spec.Egress[0].To {
			selector, _ := metav1.LabelSelectorAsSelector(to.PodSelector)
			reachesPeer = reachesPeer || selector.Matches(peerLabels)
		}
		if !reachesPeer {
			t.Fatal("the witness's egress to the peer is not allowed, so a refusal would not isolate ingress")
		}
		joined := " " + strings.Join(measured, " ") + " "
		for _, want := range []string{" kubernetes.default 443 ", " 172.16.0.10 443 ", " 169.254.169.254 80 ", " fd00:ec2::254 80 ", " 169.254.170.23 80 "} {
			if !strings.Contains(joined, want) {
				t.Errorf("measurement %q lacks %q", joined, want)
			}
		}
	}
}

// The EKS network-policy agent shares policy among Pods whose names match up
// to the last '-', so no two probe roles may share that part.
func TestProbeRolesNeverShareANamePrefix(t *testing.T) {
	_, kube := boundaryFixture()
	p := &productionBoundary{kube: kube, cfg: productionBoundaryConfig(), required: "controller.example:443", api: "kubernetes.default:443"}
	ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
	defer cancel()
	r := CloudNodeReservation{NodeName: "node", NodeUID: "uid", Team: "team", JobName: "job", ReservationID: "r"}
	owners := map[string]string{}
	for range 20 {
		for role, pod := range map[string]*corev1.Pod{"control": p.canary(ctx, r, "nonce"), "peer": p.peer(ctx, r, "nonce"), "witness": p.witness(ctx, r, "nonce", "10.0.0.1", "10.0.0.2"), "measure": p.measure(ctx, r, nil, "10.0.0.1:18888")} {
			prefix := pod.Name[:strings.LastIndex(pod.Name, "-")]
			if owner, seen := owners[prefix]; seen && owner != role {
				t.Fatalf("%s and %s share the name prefix %q", owner, role, prefix)
			}
			owners[prefix] = role
			if !strings.HasPrefix(pod.Name, "sw-boundary-") || pod.Spec.Containers[0].Name != role {
				t.Fatalf("%s probe is named %q with container %q", role, pod.Name, pod.Spec.Containers[0].Name)
			}
		}
	}
	if len(owners) != 4 {
		t.Fatalf("probe name prefixes = %v", owners)
	}
}
