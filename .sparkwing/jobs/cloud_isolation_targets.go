package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"sparkwing-pipelines/isolationprobe"
	"strconv"
	"strings"
)

type kubeList[T any] struct {
	Items []T `json:"items"`
}

type kubeMeta struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	UID             string            `json:"uid"`
	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
	OwnerReferences []kubeOwner       `json:"ownerReferences"`
}

type kubeOwner struct {
	Kind       string `json:"kind"`
	UID        string `json:"uid"`
	Controller *bool  `json:"controller"`
}

// safety: the controller reference names the object, by the UID the API server assigned it, that
// manages the pod, which is the one tie a pod's creator cannot pick by naming a label.
func (m kubeMeta) controller() (kubeOwner, bool) {
	for _, o := range m.OwnerReferences {
		if o.Controller != nil && *o.Controller && o.UID != "" {
			return o, true
		}
	}
	return kubeOwner{}, false
}

type kubeProbe struct {
	HTTPGet *struct {
		Port json.RawMessage `json:"port"`
	} `json:"httpGet"`
}

type kubeContainer struct {
	Name    string   `json:"name"`
	Image   string   `json:"image"`
	Command []string `json:"command"`
	Args    []string `json:"args"`
	Ports   []struct {
		ContainerPort int    `json:"containerPort"`
		Protocol      string `json:"protocol"`
	} `json:"ports"`
	LivenessProbe   *kubeProbe `json:"livenessProbe"`
	ReadinessProbe  *kubeProbe `json:"readinessProbe"`
	SecurityContext *struct {
		Privileged               *bool `json:"privileged"`
		AllowPrivilegeEscalation *bool `json:"allowPrivilegeEscalation"`
		ReadOnlyRootFilesystem   *bool `json:"readOnlyRootFilesystem"`
		Capabilities             *struct {
			Add  []string `json:"add"`
			Drop []string `json:"drop"`
		} `json:"capabilities"`
		SeccompProfile *struct {
			Type             string `json:"type"`
			LocalhostProfile string `json:"localhostProfile"`
		} `json:"seccompProfile"`
		ProcMount string `json:"procMount"`
	} `json:"securityContext"`
}

type kubePod struct {
	rawSpec  json.RawMessage
	Metadata kubeMeta `json:"metadata"`
	Spec     struct {
		NodeName                     string `json:"nodeName"`
		HostNetwork                  bool   `json:"hostNetwork"`
		HostPID                      bool   `json:"hostPID"`
		HostIPC                      bool   `json:"hostIPC"`
		HostUsers                    *bool  `json:"hostUsers"`
		AutomountServiceAccountToken *bool  `json:"automountServiceAccountToken"`
		SecurityContext              *struct {
			RunAsUser    *int64 `json:"runAsUser"`
			RunAsGroup   *int64 `json:"runAsGroup"`
			RunAsNonRoot *bool  `json:"runAsNonRoot"`
		} `json:"securityContext"`
		Volumes []struct {
			Name     string           `json:"name"`
			HostPath *json.RawMessage `json:"hostPath"`
		} `json:"volumes"`
		Containers     []kubeContainer `json:"containers"`
		InitContainers []kubeContainer `json:"initContainers"`
	} `json:"spec"`
	Status struct {
		Phase      string `json:"phase"`
		PodIP      string `json:"podIP"`
		HostIP     string `json:"hostIP"`
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	} `json:"status"`
}

// UnmarshalJSON retains the full spec so capacity grading cannot overlook fields outside the
// discovery projection.
func (p *kubePod) UnmarshalJSON(data []byte) error {
	type plain kubePod
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw struct {
		Spec json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*p = kubePod(decoded)
	p.rawSpec = raw.Spec
	return nil
}

func (p kubePod) ready() bool {
	for _, c := range p.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}

type kubeService struct {
	Metadata kubeMeta `json:"metadata"`
	Spec     struct {
		ClusterIP string `json:"clusterIP"`
		Ports     []struct {
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
		} `json:"ports"`
	} `json:"spec"`
}

type kubeEndpointSlice struct {
	Endpoints []struct {
		Addresses []string `json:"addresses"`
	} `json:"endpoints"`
	Ports []struct {
		Port int `json:"port"`
	} `json:"ports"`
}

type kubeNode struct {
	Metadata kubeMeta `json:"metadata"`
	Status   struct {
		Addresses []struct {
			Type    string `json:"type"`
			Address string `json:"address"`
		} `json:"addresses"`
	} `json:"status"`
}

func (n kubeNode) internalIPs() []string {
	var ips []string
	for _, a := range n.Status.Addresses {
		if a.Type == "InternalIP" {
			ips = append(ips, a.Address)
		}
	}
	return ips
}

type kubeJob struct {
	Metadata kubeMeta `json:"metadata"`
}

// safety: the launcher's award binding is the only record tying a Kubernetes Job to a run and a
// job; its fields carry no JSON tags, so the names are the Go fields'.
type awardBinding struct {
	Kind             string `json:"Kind"`
	Team             string `json:"Team"`
	RunID            string `json:"RunID"`
	NodeID           string `json:"NodeID"`
	UnitID           string `json:"UnitID"`
	Generation       int64  `json:"Generation"`
	Attempt          int64  `json:"Attempt"`
	SourceNodeID     string `json:"SourceNodeID"`
	SourceGeneration int64  `json:"SourceGeneration"`
}

const (
	awardBindingAnnotation = "sparkwing.dev/award-binding"
	jobPodLabel            = "sparkwing.dev/job"
	teamNodeLabel          = "node-restriction.kubernetes.io/sparkwing-team-node"
	usedTeamNodeLabel      = "node-restriction.kubernetes.io/sparkwing-used-team"
	usedRunNodeLabel       = "node-restriction.kubernetes.io/sparkwing-used-run"
	poolNamespaceLabel     = "sparkwing.dev/launcher-namespace"
	nodePoolLabel          = "karpenter.sh/nodepool"
	nodeClassLabel         = "karpenter.k8s.aws/ec2nodeclass"
)

// safety: the launcher labels a team's nodes with this hash of the job namespace and team, and the
// conformance check recomputes it rather than trusting a label it is judging.
func teamNodeValue(namespace, team string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + team))
	return "sha256-" + hex.EncodeToString(sum[:])[:40]
}

// safety: the launcher fences a node to the one run it first served with this hash of the job
// namespace and run ID, recomputed here for the same reason.
func runNodeValue(namespace, runID string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + runID))
	return "run-" + hex.EncodeToString(sum[:20])
}

func decodeList[T any](raw string) ([]T, error) {
	var l kubeList[T]
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		return nil, err
	}
	return l.Items, nil
}

func probePort(p *kubeProbe) int {
	if p == nil || p.HTTPGet == nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(p.HTTPGet.Port)))
	if err != nil {
		return 0
	}
	return n
}

// safety: only ports something is known to serve count as listening, so a refusal there is
// judged as the packet reaching the host; probe ports come from the kubelet's own checks. A running
// pod on its node's network, or without an address, cannot be probed as a pod, so it is returned
// for discovery to refuse instead of being skipped.
func podTargets(item, what string, pods []kubePod, skipPorts map[int]bool) ([]isolationprobe.Target, []string) {
	var out []isolationprobe.Target
	var untargetable []string
	for _, p := range pods {
		if p.Status.Phase != "Running" {
			continue
		}
		if p.Status.PodIP == "" || p.Spec.HostNetwork {
			untargetable = append(untargetable, p.Metadata.Name)
			continue
		}
		ports := map[int]bool{}
		for _, c := range p.Spec.Containers {
			for _, port := range c.Ports {
				if port.Protocol == "" || port.Protocol == "TCP" {
					ports[port.ContainerPort] = true
				}
			}
			for _, probe := range []*kubeProbe{c.LivenessProbe, c.ReadinessProbe} {
				if port := probePort(probe); port > 0 {
					ports[port] = true
				}
			}
		}
		for _, port := range sortedPorts(ports) {
			if skipPorts[port] {
				continue
			}
			out = append(out, isolationprobe.Target{
				Item: item, Name: what + " pod " + p.Metadata.Name, Address: net.JoinHostPort(p.Status.PodIP, strconv.Itoa(port)),
				Expect: isolationprobe.ExpectBlocked, Listening: p.ready(),
			})
		}
	}
	return out, untargetable
}

func sortedPorts(ports map[int]bool) []int {
	out := make([]int, 0, len(ports))
	for p := range ports {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

func serviceTargets(item, what string, services []kubeService) []isolationprobe.Target {
	var out []isolationprobe.Target
	for _, s := range services {
		if s.Spec.ClusterIP == "" || s.Spec.ClusterIP == "None" {
			continue
		}
		for _, port := range s.Spec.Ports {
			if port.Protocol != "" && port.Protocol != "TCP" {
				continue
			}
			out = append(out, isolationprobe.Target{
				Item: item, Name: what + " service " + s.Metadata.Name, Address: net.JoinHostPort(s.Spec.ClusterIP, strconv.Itoa(port.Port)),
				Expect: isolationprobe.ExpectBlocked, Listening: true,
			})
		}
	}
	return out
}

// safety: a node's kubelet always serves 10250, so it is a known listener; the other ports are
// probed without that assumption.
func nodeTargets(item, what string, ips []string) []isolationprobe.Target {
	var out []isolationprobe.Target
	for _, ip := range ips {
		at := func(port string) string { return net.JoinHostPort(ip, port) }
		out = append(out,
			isolationprobe.Target{Item: item, Name: what + " kubelet", Address: at("10250"), Expect: isolationprobe.ExpectBlocked, Listening: true, Grade: isolationprobe.GradeKubelet},
			isolationprobe.Target{Item: item, Name: what + " kubelet read-only port", Address: at("10255"), Expect: isolationprobe.ExpectBlocked},
			isolationprobe.Target{Item: item, Name: what + " kube-proxy metrics", Address: at("10249"), Expect: isolationprobe.ExpectBlocked},
			isolationprobe.Target{Item: item, Name: what + " kube-proxy health", Address: at("10256"), Expect: isolationprobe.ExpectBlocked},
			isolationprobe.Target{Item: item, Name: what + " ssh", Address: at("22"), Expect: isolationprobe.ExpectBlocked},
			isolationprobe.Target{Item: item, Name: what + " VPC CNI metrics", Address: at("61678"), Expect: isolationprobe.ExpectBlocked},
			isolationprobe.Target{Item: item, Name: what + " VPC CNI introspection", Address: at("61679"), Expect: isolationprobe.ExpectBlocked},
			isolationprobe.Target{Item: item, Name: what + " network policy agent metrics", Address: at("61680"), Expect: isolationprobe.ExpectBlocked},
		)
	}
	return out
}

// safety: link-local and unique-local metadata endpoints, including the Pod Identity agent and the
// ECS credential address, sit behind the fence on every Cloud node.
func metadataTargets() []isolationprobe.Target {
	blocked := func(item, name, addr string, listening bool, grade isolationprobe.Grade) isolationprobe.Target {
		return isolationprobe.Target{Item: item, Name: name, Address: addr, Expect: isolationprobe.ExpectBlocked, Listening: listening, Grade: grade}
	}
	return []isolationprobe.Target{
		blocked("C4", "IMDS", "169.254.169.254:80", true, isolationprobe.GradeIMDS),
		blocked("C4", "EKS Pod Identity agent", "169.254.170.23:80", false, ""),
		blocked("C4", "ECS credential endpoint", "169.254.170.2:80", false, ""),
		blocked("C4", "Route 53 Resolver link-local address", "169.254.169.253:53", false, ""),
		blocked("C5", "IMDS over IPv6", "[fd00:ec2::254]:80", false, isolationprobe.GradeIMDS),
		blocked("C5", "EKS Pod Identity agent over IPv6", "[fd00:ec2::23]:80", false, ""),
	}
}

// safety: addresses in 10/8 outside the VPC answer nowhere, so a refusal there cannot tell the
// fence from a missing route; they are recorded, and the deployment's own listeners decide C6.
var outsideVPCPrivate = []string{"10.1.0.1:443", "10.100.0.1:443", "10.200.0.1:443", "10.255.255.254:443"}

type peerFacts struct {
	Label      string
	Team       string
	RunID      string
	PodName    string
	PodIP      string
	HostIP     string
	ListenPort int
	Canary     string
}

// safety: a peer's listener is known to answer because its own self-connect passed, so a refusal
// there is a breach; its Docker ports have no listener by design.
func peerTargets(p peerFacts) []isolationprobe.Target {
	at := func(port int) string { return net.JoinHostPort(p.PodIP, strconv.Itoa(port)) }
	what := p.Label + " (" + p.Team + ")"
	out := []isolationprobe.Target{
		{Item: "C9", Name: "peer " + what + " listener", Address: at(p.ListenPort), Expect: isolationprobe.ExpectBlocked, Listening: true},
		{Item: "C9", Name: "peer " + what + " Docker API", Address: at(2375), Expect: isolationprobe.ExpectBlocked},
		{Item: "C9", Name: "peer " + what + " Docker TLS API", Address: at(2376), Expect: isolationprobe.ExpectBlocked},
	}
	return append(out, nodeTargets("C8", "peer "+what+" node", []string{p.HostIP})...)
}

type targetInputs struct {
	Namespace          string
	ControllerPods     []kubePod
	ControllerServices []kubeService
	DNSPods            []kubePod
	DNSServices        []kubeService
	APIService         kubeService
	APIEndpoints       []kubeEndpointSlice
	InfraNodes         []kubeNode
	IngressPods        []kubePod
	PublicAPI          string
	Database           string
	VPCResolver        string
}

// safety: each group is a boundary the fence must hold, so discovery fails unless every group
// yields something to probe, and a running member it cannot probe fails it too, rather than letting
// the run pass without that boundary. Cluster DNS answers on port 53 by design; the probe's cluster
// DNS lookup is that port's positive control, and its other ports must stay blocked.
func staticTargets(in targetInputs) ([]isolationprobe.Target, error) {
	var errs []error
	need := func(ok bool, what string) {
		if !ok {
			errs = append(errs, fmt.Errorf("discovery found no %s", what))
		}
	}
	need(in.Database != "", "controller database endpoint")
	need(in.VPCResolver != "", "VPC resolver address")
	need(in.APIService.Spec.ClusterIP != "", "ClusterIP on the kubernetes Service")
	need(in.PublicAPI != "", "public kube API endpoint")
	need(in.Namespace != "", "controller namespace for the kube API grade")
	group := func(what string, targets []isolationprobe.Target, untargetable []string) []isolationprobe.Target {
		if len(untargetable) > 0 {
			errs = append(errs, fmt.Errorf("discovery cannot probe %s %s: each runs on its node's network or has no address", what, strings.Join(untargetable, ", ")))
		}
		if len(targets) == 0 {
			errs = append(errs, fmt.Errorf("discovery found no %s to probe", what))
		}
		return targets
	}
	t, bad := podTargets("C6", "controller namespace", in.ControllerPods, nil)
	controllerPods := group("controller namespace pods", t, bad)
	controllerServices := group("controller namespace services", serviceTargets("C6", "controller namespace", in.ControllerServices), nil)
	t, bad = podTargets("C6", "cluster DNS", in.DNSPods, map[int]bool{53: true})
	dnsPods := group("cluster DNS pods", t, bad)
	dnsServices, dnsAddressed := dnsServiceTargets(in.DNSServices)
	need(dnsAddressed, "cluster DNS service with a ClusterIP")
	t, bad = podTargets("C6", "ingress", in.IngressPods, nil)
	ingressPods := group("ingress pods", t, bad)
	var infra []isolationprobe.Target
	var unaddressed []string
	for _, n := range in.InfraNodes {
		t := nodeTargets("C6", "controller node "+n.Metadata.Name, n.internalIPs())
		if len(t) == 0 {
			unaddressed = append(unaddressed, n.Metadata.Name)
		}
		infra = append(infra, t...)
	}
	infra = group("controller nodes", infra, unaddressed)
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	var out []isolationprobe.Target
	out = append(out, metadataTargets()...)
	out = append(out, controllerPods...)
	out = append(out, controllerServices...)
	out = append(out, dnsPods...)
	out = append(out, dnsServices...)
	out = append(out, ingressPods...)
	out = append(out, infra...)
	out = append(out,
		isolationprobe.Target{Item: "C6", Name: "controller database", Address: in.Database, Expect: isolationprobe.ExpectBlocked, Listening: true},
		isolationprobe.Target{Item: "C6", Name: "VPC resolver", Address: net.JoinHostPort(in.VPCResolver, "53"), Expect: isolationprobe.ExpectBlocked, Listening: true},
	)
	for _, addr := range outsideVPCPrivate {
		out = append(out, isolationprobe.Target{Item: "C6", Name: "10/8 outside the VPC", Address: addr, Expect: isolationprobe.ExpectObserved})
	}
	kube := func(name, addr string, expect isolationprobe.Expect) isolationprobe.Target {
		return isolationprobe.Target{Item: "C7", Name: name, Address: addr, Expect: expect, Listening: true, Grade: isolationprobe.GradeKubeAPI, Namespace: in.Namespace}
	}
	out = append(out, kube("kube API service", net.JoinHostPort(in.APIService.Spec.ClusterIP, "443"), isolationprobe.ExpectBlocked))
	endpoints := 0
	for _, slice := range in.APIEndpoints {
		port := 443
		if len(slice.Ports) > 0 && slice.Ports[0].Port > 0 {
			port = slice.Ports[0].Port
		}
		for _, e := range slice.Endpoints {
			for _, addr := range e.Addresses {
				endpoints++
				out = append(out, kube("kube API private endpoint", net.JoinHostPort(addr, strconv.Itoa(port)), isolationprobe.ExpectBlocked))
			}
		}
	}
	if endpoints == 0 {
		return nil, errors.New("the kubernetes Service has no endpoints")
	}
	out = append(out, kube("kube API public endpoint", net.JoinHostPort(in.PublicAPI, "443"), isolationprobe.ExpectDenied))
	for _, item := range []string{"C4", "C5", "C6", "C7"} {
		if !hasJudgedItem(out, item) {
			return nil, fmt.Errorf("discovery found no %s targets", item)
		}
	}
	return out, nil
}

func dnsServiceTargets(services []kubeService) ([]isolationprobe.Target, bool) {
	var out []isolationprobe.Target
	addressed := false
	for _, s := range services {
		if s.Spec.ClusterIP == "" || s.Spec.ClusterIP == "None" {
			continue
		}
		addressed = true
		for _, port := range s.Spec.Ports {
			if port.Port != 53 && (port.Protocol == "" || port.Protocol == "TCP") {
				out = append(out, isolationprobe.Target{Item: "C6", Name: "cluster DNS service metrics", Address: net.JoinHostPort(s.Spec.ClusterIP, strconv.Itoa(port.Port)), Expect: isolationprobe.ExpectBlocked, Listening: true})
			}
		}
	}
	return out, addressed
}

func hasJudgedItem(targets []isolationprobe.Target, item string) bool {
	for _, t := range targets {
		if t.Item == item && t.Expect != isolationprobe.ExpectObserved {
			return true
		}
	}
	return false
}

// safety: kubectl config view redacts credentials but keeps each cluster's server, so the public
// endpoint is read from the operator's kubeconfig instead of being written into this repository.
func contextServerHost(configJSON, context string) (string, error) {
	var cfg struct {
		Contexts []struct {
			Name    string `json:"name"`
			Context struct {
				Cluster string `json:"cluster"`
			} `json:"context"`
		} `json:"contexts"`
		Clusters []struct {
			Name    string `json:"name"`
			Cluster struct {
				Server string `json:"server"`
			} `json:"cluster"`
		} `json:"clusters"`
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return "", fmt.Errorf("parse kubeconfig: %w", err)
	}
	for _, c := range cfg.Contexts {
		if c.Name != context {
			continue
		}
		for _, cl := range cfg.Clusters {
			if cl.Name != c.Context.Cluster {
				continue
			}
			u, err := url.Parse(cl.Cluster.Server)
			if err != nil || u.Hostname() == "" {
				return "", fmt.Errorf("context %s has no usable server", context)
			}
			return u.Hostname(), nil
		}
	}
	return "", fmt.Errorf("kubeconfig has no context %s", context)
}
