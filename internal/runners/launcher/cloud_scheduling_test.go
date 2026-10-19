package launcher

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func FuzzCloudSchedulingPreservesTeamAndDocker(f *testing.F) {
	f.Add("team", uint8(0))
	f.Add("other-team", uint8(1))
	f.Add("tenant/with-symbols", uint8(2))
	f.Fuzz(func(t *testing.T, team string, kind uint8) {
		if team == "" {
			return
		}
		l, _, boundary, control := fixture()
		award := *control.award
		award.Binding.Team = team
		switch kind % 3 {
		case 0:
			award.Binding.Kind = CloudPlan
			award.Binding.NodeID = ""
		case 1:
			award.Binding.Kind = CloudEval
			award.Binding.SourceNodeID = award.Binding.NodeID
			award.Binding.SourceGeneration = award.Binding.Generation
		}
		r, err := BuildJob(t.Context(), time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), l.Config, award, boundary)
		if err != nil {
			t.Fatal(err)
		}
		pod := r.Job.Spec.Template
		if pod.Annotations[doNotDisrupt] != "true" || pod.Spec.NodeName != r.Reservation.NodeName || pod.Spec.NodeSelector[TeamNodeLabel] != TeamNodeValue(team) {
			t.Fatalf("placement protection missing: %+v", pod)
		}
		if pod.Spec.HostUsers == nil || *pod.Spec.HostUsers || *pod.Spec.SecurityContext.RunAsUser != 0 || pod.Spec.InitContainers[0].SecurityContext.Privileged != nil || len(pod.Spec.InitContainers[0].SecurityContext.Capabilities.Add) != 3 {
			t.Fatal("unprivileged DinD in a user-namespaced Pod missing")
		}
		teamTaint := false
		found := false
		for _, toleration := range pod.Spec.Tolerations {
			if toleration.Key == TeamNodeLabel && toleration.Value == TeamNodeValue(team) && toleration.Operator == corev1.TolerationOpEqual && toleration.Effect == corev1.TaintEffectNoSchedule {
				teamTaint = true
			}
			if toleration.Key == "sparkwing.dev/cpu-band" && toleration.Operator == corev1.TolerationOpExists && toleration.Effect == corev1.TaintEffectNoSchedule {
				found = true
			}
		}
		if !found || !teamTaint {
			t.Fatal("CPU band taint not tolerated")
		}
		r.Job.Spec.Suspend = ptr(true)
		if err := validateAdmittedJob(r.Job, r.Job.DeepCopy()); err != nil {
			t.Fatal(err)
		}
		admitted := r.Job.DeepCopy()
		switch kind % 3 {
		case 0:
			delete(admitted.Spec.Template.Annotations, doNotDisrupt)
		case 1:
			admitted.Spec.Template.Spec.Tolerations = nil
		case 2:
			admitted.Spec.Template.Spec.NodeSelector[TeamNodeLabel] = TeamNodeValue(team + "foreign")
		}
		if err := validateAdmittedJob(r.Job, admitted); err == nil {
			t.Fatal("admission weakened Cloud protection")
		}
	})
}
