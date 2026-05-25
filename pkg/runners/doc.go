// The runners: section names the runners a pipeline can dispatch jobs
// to. Each entry declares the labels it advertises and, for
// cluster-backed types, the spec used to materialize a runner pod.
// Job-level selection (Job.Requires / Prefers / WhenRunner) matches
// against these advertised labels.
//
// Implicit local: if the section declares no runner named "local",
// resolution synthesizes one carrying labels for the current host's OS
// and architecture. A declared "local" entry overrides it.
//
// One entry is a [Runner] with an optional [Spec] (Kubernetes-only pod
// placement + [Toleration]s + [Resources]).
//
// # Shape (yaml)
//
//	# .sparkwing/sparkwing.yaml
//	runners:
//	  local:
//	    type: local
//	    labels: [local, "os=darwin"]
//	  cloud-linux:
//	    type: kubernetes
//	    profile: shared        # profile name from profiles.yaml
//	    labels: [cloud-linux, "os=linux"]
//	    spec:
//	      nodeSelector: { karpenter.sh/nodepool: general }
//	      resources:
//	        requests: { cpu: 2, memory: 4Gi }
//	  mac-mini:
//	    type: static
//	    labels: [mac-mini, "os=macos"]
package runners
