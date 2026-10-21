package launcher

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const boundaryControlLabel = "sparkwing.dev/boundary-control"

// safety: admission pins these commands, so an admission mutation cannot change what a probe runs.
// Job Pods hold no node root, so every target is measured from the Pod
// network namespace, the only one Job code can reach. The witness may send to
// both listeners: reaching the control proves its egress, so failing to reach
// the readiness-confirmed peer, which carries Job labels, proves Job ingress
// is denied rather than absent.
var (
	listenerCommand = []string{"/bin/bash", "-ceu", "exec nc -lk 18888"}
	peerReadiness   = []string{"nc", "-z", "127.0.0.1", "18888"}
	witnessCommand  = []string{"/bin/bash", "-ceu", `nc -z -w 3 "$1" 18888
if nc -z -w 3 "$2" 18888; then echo "Cloud Job ingress is not denied" >&2; exit 1; fi`, "boundary-control"}
	measureCommand = []string{"/bin/bash", "-ceu", `command -v nc >/dev/null
control_host="$1"; control_port="$2"; shift 2
required_host="$1"; required_port="$2"; shift 2
nc -z -w 3 "$required_host" "$required_port"
if nc -z -w 3 "$control_host" "$control_port"; then echo "Cloud default-deny egress is not enforced" >&2; exit 1; fi
while (( $# )); do
 host="$1"; port="$2"; shift 2
 if nc -z -w 3 "$host" "$port"; then echo "Cloud pod egress escaped to $host:$port" >&2; exit 1; fi
done`, "boundary-probe"}
)

// NewProductionBoundary measures Job networking with live canaries before authorizing Cloud execution.
func NewProductionBoundary(kube kubernetes.Interface, karpenter dynamic.Interface, cfg CloudJobConfig, apiServer string) (*KubernetesBoundary, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	required, err := boundaryEndpoint(cfg.ControllerURL)
	if err != nil {
		return nil, err
	}
	api, err := boundaryEndpoint(apiServer)
	if err != nil {
		return nil, err
	}
	if kube == nil {
		return nil, errors.New("Cloud boundary requires Kubernetes")
	}
	p := &productionBoundary{kube: kube, cfg: cfg, required: required, api: api}
	return &KubernetesBoundary{Kube: kube, Karpenter: karpenter, Namespace: cfg.Namespace, Probe: p.probe, Clear: p.stopProbes, StopProbes: p.stopProbes}, nil
}

type productionBoundary struct {
	kube          kubernetes.Interface
	cfg           CloudJobConfig
	required, api string
}

func boundaryEndpoint(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return "", errors.New("Cloud probe endpoint must be an HTTP(S) origin")
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}

func (p *productionBoundary) snapshot(ctx context.Context, r CloudNodeReservation) (string, []string, error) {
	node, err := p.kube.CoreV1().Nodes().Get(ctx, r.NodeName, metav1.GetOptions{})
	if err != nil {
		return "", nil, err
	}
	var held CloudNodeReservation
	if err = json.Unmarshal([]byte(node.Annotations[reservationAnnotation]), &held); err != nil {
		return "", nil, err
	}
	if string(node.UID) != r.NodeUID || held != r || node.Annotations[reservationNamespaceAnnotation] != p.cfg.Namespace || node.Labels[TeamNodeLabel] != TeamNodeValue(p.cfg.Namespace, r.Team) {
		return "", nil, errors.New("Cloud probe reservation changed")
	}
	policies, err := p.kube.NetworkingV1().NetworkPolicies(p.cfg.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", nil, err
	}
	workloadLabels := labels.Set(map[string]string{"app": "sparkwing-job", "app.kubernetes.io/managed-by": managedByLauncher, JobLabel: r.JobName, TeamLabel: TeamNodeValue(p.cfg.Namespace, r.Team)})
	denyEgress, denyIngress := false, false
	for _, policy := range policies.Items {
		selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
		if err != nil {
			return "", nil, err
		}
		if selector.Matches(workloadLabels) {
			denyEgress = denyEgress || slices.Contains(policy.Spec.PolicyTypes, networkingv1.PolicyTypeEgress) && len(policy.Spec.Egress) == 0
			denyIngress = denyIngress || slices.Contains(policy.Spec.PolicyTypes, networkingv1.PolicyTypeIngress) && len(policy.Spec.Ingress) == 0
		}
	}
	if !denyEgress || !denyIngress {
		return "", nil, errors.New("Cloud namespace requires selecting default-deny egress and ingress policies")
	}
	pods, err := p.kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", nil, err
	}
	// safety: the instance metadata service and the Pod Identity agent hand out
	// node and workload credentials; operator egress rules must still refuse them.
	targets := []string{p.api, "169.254.169.254:80", "[fd00:ec2::254]:80", "169.254.170.23:80"}
	// safety: the in-cluster API address is a Service; the kubelet dials the
	// addresses behind it, which pod egress must refuse as well.
	api, err := p.kube.DiscoveryV1().EndpointSlices(metav1.NamespaceDefault).List(ctx, metav1.ListOptions{LabelSelector: discoveryv1.LabelServiceName + "=kubernetes"})
	if err != nil {
		return "", nil, err
	}
	for _, slice := range api.Items {
		for _, port := range slice.Ports {
			for _, endpoint := range slice.Endpoints {
				for _, address := range endpoint.Addresses {
					if port.Port != nil {
						targets = append(targets, net.JoinHostPort(address, strconv.Itoa(int(*port.Port))))
					}
				}
			}
		}
	}
	var inventory []string
	for _, pod := range pods.Items {
		if !daemonPod(pod) {
			continue
		}
		for _, container := range append(slices.Clone(pod.Spec.InitContainers), pod.Spec.Containers...) {
			for _, port := range container.Ports {
				protocol := port.Protocol
				if protocol == "" {
					protocol = corev1.ProtocolTCP
				}
				inventory = append(inventory, fmt.Sprintf("%s/%s/%s:%d/%s", pod.Namespace, pod.Name, container.Name, port.ContainerPort, protocol))
				if protocol != corev1.ProtocolTCP || port.ContainerPort == 53 {
					continue
				}
				if pod.Status.PodIP != "" {
					targets = append(targets, net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(int(port.ContainerPort))))
				}
				if port.HostPort > 0 && pod.Status.HostIP != "" {
					targets = append(targets, net.JoinHostPort(pod.Status.HostIP, strconv.Itoa(int(port.HostPort))))
				}
			}
		}
	}
	nodes, err := p.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", nil, err
	}
	for _, node := range nodes.Items {
		for _, address := range node.Status.Addresses {
			if address.Type == corev1.NodeInternalIP && node.Status.DaemonEndpoints.KubeletEndpoint.Port > 0 {
				targets = append(targets, net.JoinHostPort(address.Address, strconv.Itoa(int(node.Status.DaemonEndpoints.KubeletEndpoint.Port))))
			}
		}
	}
	slices.Sort(targets)
	targets = slices.Compact(targets)
	if slices.Contains(targets, p.required) {
		return "", nil, fmt.Errorf("conflicting required and forbidden Cloud destination: %s", p.required)
	}
	slices.Sort(inventory)
	slices.SortFunc(policies.Items, func(a, b networkingv1.NetworkPolicy) int { return strings.Compare(a.Name, b.Name) })
	raw, err := json.Marshal(struct {
		Policies           []networkingv1.NetworkPolicy
		Inventory, Targets []string
	}{policies.Items, inventory, targets})
	if err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), targets, nil
}

func (p *productionBoundary) probe(ctx context.Context, r CloudNodeReservation) (evidence CloudNetworkEvidence, err error) {
	node, err := p.kube.CoreV1().Nodes().Get(ctx, r.NodeName, metav1.GetOptions{})
	if err != nil {
		return evidence, err
	}
	var binding CloudJobBinding
	if err = json.Unmarshal([]byte(node.Annotations[bindingAnnotation]), &binding); err != nil {
		return evidence, err
	}
	deadline, err := cloudDeadline(time.Now(), binding)
	if err != nil {
		return evidence, err
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	revision, denied, err := p.snapshot(ctx, r)
	if err != nil {
		return evidence, err
	}
	var pods []*corev1.Pod
	var ingress *networkingv1.NetworkPolicy
	defer func() {
		for _, pod := range pods {
			err = errors.Join(err, p.deletePod(ctx, pod))
		}
		if ingress != nil {
			deletion := p.kube.NetworkingV1().NetworkPolicies(p.cfg.Namespace).Delete(ctx, ingress.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &ingress.UID}})
			if !apierrors.IsNotFound(deletion) {
				err = errors.Join(err, deletion)
			}
		}
		if err == nil {
			current, _, check := p.snapshot(ctx, r)
			err = check
			if err == nil && current != revision {
				err = errors.New("Cloud enforcement inputs changed during probes")
			}
		}
	}()
	nonce := strings.ToLower(rand.Text())
	control := p.canary(ctx, r, nonce)
	admitted, err := p.kube.CoreV1().Pods(p.cfg.Namespace).Create(ctx, control, metav1.CreateOptions{})
	if err != nil {
		return evidence, err
	}
	pods = append(pods, admitted)
	if err = p.validateCanary(control, admitted); err != nil {
		return evidence, err
	}
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: control.Name, Namespace: p.cfg.Namespace, Annotations: control.Annotations}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{boundaryControlLabel: nonce}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{}}}}
	policy.Spec.PolicyTypes = append(policy.Spec.PolicyTypes, networkingv1.PolicyTypeEgress)
	policy.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{PodSelector: &policy.Spec.PodSelector}, {PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{boundaryControlLabel: nonce + "-peer"}}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: ptr(corev1.ProtocolTCP), Port: ptr(intstr.FromInt32(18888))}}}}
	ingress, err = p.kube.NetworkingV1().NetworkPolicies(p.cfg.Namespace).Create(ctx, policy, metav1.CreateOptions{})
	if err != nil {
		return evidence, err
	}
	if ingress.UID == "" || !equality.Semantic.DeepEqual(policy.Spec, ingress.Spec) {
		return evidence, errors.New("Kubernetes changed Cloud control ingress policy")
	}
	ready, err := p.waitPod(ctx, admitted, true)
	if err != nil {
		return evidence, err
	}
	if net.ParseIP(ready.Status.PodIP) == nil {
		return evidence, errors.New("Cloud canary has no observed IP address")
	}
	peer := p.peer(ctx, r, nonce)
	admitted, err = p.kube.CoreV1().Pods(p.cfg.Namespace).Create(ctx, peer, metav1.CreateOptions{})
	if err != nil {
		return evidence, err
	}
	pods = append(pods, admitted)
	if err = p.validateCanary(peer, admitted); err != nil {
		return evidence, err
	}
	peerReady, err := p.waitPod(ctx, admitted, true)
	if err != nil {
		return evidence, err
	}
	if net.ParseIP(peerReady.Status.PodIP) == nil {
		return evidence, errors.New("Cloud peer canary has no observed IP address")
	}
	witness := p.witness(ctx, r, nonce, ready.Status.PodIP, peerReady.Status.PodIP)
	admitted, err = p.kube.CoreV1().Pods(p.cfg.Namespace).Create(ctx, witness, metav1.CreateOptions{})
	if err != nil {
		return evidence, err
	}
	pods = append(pods, admitted)
	if err = p.validateCanary(witness, admitted); err != nil {
		return evidence, err
	}
	if _, err = p.waitPod(ctx, admitted, false); err != nil {
		return evidence, err
	}
	measure := p.measure(ctx, r, denied, net.JoinHostPort(ready.Status.PodIP, "18888"))
	admitted, err = p.kube.CoreV1().Pods(p.cfg.Namespace).Create(ctx, measure, metav1.CreateOptions{})
	if err != nil {
		return evidence, err
	}
	pods = append(pods, admitted)
	if err = p.validateCanary(measure, admitted); err != nil {
		return evidence, err
	}
	if _, err = p.waitPod(ctx, admitted, false); err != nil {
		return evidence, err
	}
	for _, pod := range pods[:2] {
		live, check := p.kube.CoreV1().Pods(p.cfg.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if check != nil {
			return evidence, check
		}
		if live.UID != pod.UID || !podReady(live) {
			return evidence, errors.New("Cloud listener changed during enforcement probes")
		}
	}
	return CloudNetworkEvidence{Namespace: p.cfg.Namespace, NodeUID: r.NodeUID, PolicyRevision: revision, DefaultDenyEgressEnforced: true, RequiredDestinationsAllowed: true, DaemonSetPortsInventoried: true, EscapeProbePassed: true}, nil
}

// probePodSecurity and probeSecurity run every probe container unprivileged,
// with no capabilities and the runtime seccomp profile.
func probePodSecurity() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{RunAsUser: ptr(int64(65534)), RunAsGroup: ptr(int64(65534)), RunAsNonRoot: ptr(true), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
}

func probeSecurity() *corev1.SecurityContext {
	return &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
}

// boundaryName keeps each probe role's name distinct before its last '-'.
// The EKS VPC CNI network-policy agent shares one policy map among Pods in a
// namespace whose names match up to the last '-', so canaries named only
// sw-boundary-<random> made the peer inherit the control's allow-all ingress.
func boundaryName(role string) string {
	return boundaryPrefix + role + "-" + strings.ToLower(rand.Text())
}

func (p *productionBoundary) canary(ctx context.Context, r CloudNodeReservation, nonce string) *corev1.Pod {
	raw, _ := json.Marshal(r)
	deadline, _ := ctx.Deadline()
	seconds := max(int64(time.Until(deadline).Seconds()), 1)
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: boundaryName("control"), Namespace: p.cfg.Namespace, Labels: map[string]string{boundaryControlLabel: nonce, TeamLabel: TeamNodeValue(p.cfg.Namespace, r.Team)}, Annotations: map[string]string{reservationAnnotation: string(raw), doNotDisrupt: "true"}}, Spec: corev1.PodSpec{NodeName: r.NodeName, NodeSelector: map[string]string{TeamNodeLabel: TeamNodeValue(p.cfg.Namespace, r.Team)}, Tolerations: teamTolerations(p.cfg.Namespace, r.Team), ActiveDeadlineSeconds: &seconds, ServiceAccountName: ServiceAccount, AutomountServiceAccountToken: ptr(false), EnableServiceLinks: ptr(false), RestartPolicy: corev1.RestartPolicyNever, SecurityContext: probePodSecurity(), Containers: []corev1.Container{{Name: "control", Image: p.cfg.RunnerImage, Command: listenerCommand, SecurityContext: probeSecurity(), ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(18888)}}, TimeoutSeconds: 1, PeriodSeconds: 10, SuccessThreshold: 1, FailureThreshold: 3}}}}}
}

func (p *productionBoundary) witness(ctx context.Context, r CloudNodeReservation, nonce, listener, peer string) *corev1.Pod {
	pod := p.canary(ctx, r, nonce)
	container := &pod.Spec.Containers[0]
	container.Name, container.ReadinessProbe = "witness", nil
	pod.Name = boundaryName(container.Name)
	container.Command = witnessCommand
	container.Args = []string{listener, peer}
	return pod
}

// peer is a listener with a Job's labels, so only Job ingress policy governs
// it. Its readiness check runs on loopback, which no policy filters.
func (p *productionBoundary) peer(ctx context.Context, r CloudNodeReservation, nonce string) *corev1.Pod {
	pod := p.canary(ctx, r, nonce+"-peer")
	pod.Labels = map[string]string{boundaryControlLabel: nonce + "-peer", "app": "sparkwing-job", "app.kubernetes.io/managed-by": managedByLauncher, JobLabel: r.JobName + "-peer", TeamLabel: TeamNodeValue(p.cfg.Namespace, r.Team)}
	container := &pod.Spec.Containers[0]
	container.Name = "peer"
	pod.Name = boundaryName(container.Name)
	container.ReadinessProbe.ProbeHandler = corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: slices.Clone(peerReadiness)}}
	return pod
}

func (p *productionBoundary) measure(ctx context.Context, r CloudNodeReservation, denied []string, control string) *corev1.Pod {
	pod := p.canary(ctx, r, "")
	pod.Labels = map[string]string{"app": "sparkwing-job", "app.kubernetes.io/managed-by": managedByLauncher, JobLabel: r.JobName, TeamLabel: TeamNodeValue(p.cfg.Namespace, r.Team)}
	container := &pod.Spec.Containers[0]
	container.Name = "measure"
	pod.Name = boundaryName(container.Name)
	container.ReadinessProbe = nil
	container.Command = measureCommand
	for _, endpoint := range append([]string{control, p.required}, denied...) {
		host, port, _ := net.SplitHostPort(endpoint)
		container.Args = append(container.Args, host, port)
	}
	return pod
}

// safety: admission bounds probe shape; the measurement's own commands, labels and
// placement must also survive admission for its result to mean anything.
func (p *productionBoundary) validateCanary(expected, admitted *corev1.Pod) error {
	if admitted == nil || admitted.UID == "" || admitted.Name != expected.Name || admitted.Namespace != expected.Namespace || admitted.DeletionTimestamp != nil {
		return errors.New("Kubernetes changed Cloud canary identity")
	}
	annotations, labels := maps.Clone(admitted.Annotations), maps.Clone(admitted.Labels)
	delete(annotations, string(podENI))
	for _, k := range nodeTopologyLabels {
		delete(labels, k)
	}
	if !maps.Equal(labels, expected.Labels) || !maps.Equal(annotations, expected.Annotations) || admitted.Spec.NodeName != expected.Spec.NodeName || len(admitted.Spec.Containers) != len(expected.Spec.Containers) {
		return errors.New("Kubernetes changed Cloud canary metadata or placement")
	}
	for i, want := range expected.Spec.Containers {
		got := admitted.Spec.Containers[i]
		if got.Name != want.Name || got.Image != want.Image || !slices.Equal(got.Command, want.Command) || !slices.Equal(got.Args, want.Args) {
			return errors.New("Kubernetes changed Cloud canary commands")
		}
	}
	return nil
}

func podReady(pod *corev1.Pod) bool {
	return pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodRunning && slices.ContainsFunc(pod.Status.Conditions, func(c corev1.PodCondition) bool { return c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue })
}

func (p *productionBoundary) waitPod(ctx context.Context, pod *corev1.Pod, ready bool) (*corev1.Pod, error) {
	var current *corev1.Pod
	err := wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		var err error
		current, err = p.kube.CoreV1().Pods(p.cfg.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if current.UID != pod.UID || current.DeletionTimestamp != nil {
			return false, errors.New("Cloud canary was replaced or deleted")
		}
		if current.Status.Phase == corev1.PodFailed {
			return false, fmt.Errorf("Cloud boundary canary %s failed", pod.Name)
		}
		return (ready && podReady(current)) || (!ready && current.Status.Phase == corev1.PodSucceeded), nil
	})
	return current, err
}

func (p *productionBoundary) deletePod(ctx context.Context, pod *corev1.Pod) error {
	if pod == nil || pod.UID == "" || pod.Name == "" || pod.Namespace != p.cfg.Namespace {
		return errors.New("Cloud probe cleanup requires an observed namespaced Pod UID")
	}
	err := p.kube.CoreV1().Pods(p.cfg.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		current, err := p.kube.CoreV1().Pods(p.cfg.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err == nil && current.UID != pod.UID {
			return true, nil
		}
		return false, err
	})
}

func (p *productionBoundary) stopProbes(ctx context.Context, r CloudNodeReservation) error {
	pods, err := p.kube.CoreV1().Pods(p.cfg.Namespace).List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + r.NodeName})
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		var held CloudNodeReservation
		if pod.Spec.NodeName == r.NodeName && strings.HasPrefix(pod.Name, boundaryPrefix) && json.Unmarshal([]byte(pod.Annotations[reservationAnnotation]), &held) == nil && held == r {
			if err = p.deletePod(ctx, &pod); err != nil {
				return err
			}
		}
	}
	policies, err := p.kube.NetworkingV1().NetworkPolicies(p.cfg.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, policy := range policies.Items {
		var held CloudNodeReservation
		if strings.HasPrefix(policy.Name, boundaryPrefix) && json.Unmarshal([]byte(policy.Annotations[reservationAnnotation]), &held) == nil && held == r {
			err = p.kube.NetworkingV1().NetworkPolicies(p.cfg.Namespace).Delete(ctx, policy.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &policy.UID}})
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}
