package jobs

import (
	"encoding/json"
	"slices"
	"strings"
)

// safety: the discovery structs omit most Kubernetes fields. Only the launcher's capacity shape
// earns an exemption; hooks, probes, extra containers and future execution fields stay foreign.
func capacityPodShape(raw json.RawMessage) bool {
	spec, ok := capacityFields(raw, "affinity automountServiceAccountToken containers dnsPolicy enableServiceLinks hostNetwork hostPID hostIPC hostUsers nodeName nodeSelector preemptionPolicy priority restartPolicy schedulerName securityContext serviceAccount serviceAccountName terminationGracePeriodSeconds tolerations")
	if !ok {
		return false
	}
	var containers []json.RawMessage
	if json.Unmarshal(spec["containers"], &containers) != nil || len(containers) != 1 {
		return false
	}
	container, ok := capacityFields(containers[0], "name image command args imagePullPolicy resources securityContext terminationMessagePath terminationMessagePolicy")
	if !ok {
		return false
	}
	security, ok := capacityFields(container["securityContext"], "allowPrivilegeEscalation readOnlyRootFilesystem capabilities privileged")
	if !ok {
		return false
	}
	if _, ok := capacityFields(security["capabilities"], "add drop"); !ok {
		return false
	}
	if resources := container["resources"]; len(resources) != 0 {
		fields, ok := capacityFields(resources, "requests limits")
		if !ok {
			return false
		}
		for _, quantities := range fields {
			if _, ok := capacityFields(quantities, "cpu memory ephemeral-storage"); !ok {
				return false
			}
		}
	}
	podSecurity, ok := capacityFields(spec["securityContext"], "runAsUser runAsGroup runAsNonRoot seccompProfile")
	if !ok {
		return false
	}
	if profile := podSecurity["seccompProfile"]; len(profile) != 0 {
		fields, ok := capacityFields(profile, "type")
		var kind string
		if !ok || json.Unmarshal(fields["type"], &kind) != nil || kind != "RuntimeDefault" {
			return false
		}
	}
	return true
}

func capacityFields(raw json.RawMessage, allowed string) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, false
	}
	names := strings.Fields(allowed)
	for name := range fields {
		if !slices.Contains(names, name) {
			return nil, false
		}
	}
	return fields, true
}
