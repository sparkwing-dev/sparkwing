package k8s

import (
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"
)

func teamJob(t *testing.T, team string) *batchv1.Job {
	t.Helper()
	return classJob(t, Config{Image: "img", Team: team}, 4)
}

// wantTeamAffinity is the whole affinity a Job of team acme renders. Any
// change to the term, including dropping it, fails here first.
const wantTeamAffinity = `podAntiAffinity:
  requiredDuringSchedulingIgnoredDuringExecution:
  - labelSelector:
      matchExpressions:
      - key: sparkwing.dev/team
        operator: Exists
      - key: sparkwing.dev/team
        operator: NotIn
        values:
        - acme
    namespaceSelector: {}
    topologyKey: kubernetes.io/hostname
`

func TestBuildJob_RendersTeamAntiAffinity(t *testing.T) {
	job := teamJob(t, "acme")
	got, err := yaml.Marshal(job.Spec.Template.Spec.Affinity)
	if err != nil {
		t.Fatalf("marshal affinity: %v", err)
	}
	if string(got) != wantTeamAffinity {
		t.Fatalf("affinity =\n%s\nwant\n%s", got, wantTeamAffinity)
	}
}

func TestBuildJob_LabelsJobAndPodWithTeam(t *testing.T) {
	job := teamJob(t, "acme")
	if got := job.Labels[TeamLabel]; got != "acme" {
		t.Fatalf("job label %s = %q, want acme", TeamLabel, got)
	}
	if got := job.Spec.Template.Labels[TeamLabel]; got != "acme" {
		t.Fatalf("pod label %s = %q, want acme", TeamLabel, got)
	}
}

// teamRepels reports whether a pod of job's template refuses a node that runs
// a pod labeled other.
func teamRepels(t *testing.T, job *batchv1.Job, other map[string]string) bool {
	t.Helper()
	aff := job.Spec.Template.Spec.Affinity
	if aff == nil || aff.PodAntiAffinity == nil {
		return false
	}
	for _, term := range aff.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
		if term.TopologyKey != corev1.LabelHostname {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(term.LabelSelector)
		if err != nil {
			t.Fatalf("selector: %v", err)
		}
		if sel.Matches(labels.Set(other)) {
			return true
		}
	}
	return false
}

func TestBuildJob_TwoTeamsRepelEachOtherButNotThemselves(t *testing.T) {
	acme, beta := teamJob(t, "acme"), teamJob(t, "beta")
	acmePod, betaPod := acme.Spec.Template.Labels, beta.Spec.Template.Labels

	if !teamRepels(t, acme, betaPod) {
		t.Fatal("an acme Job accepts a node running a beta Job")
	}
	if !teamRepels(t, beta, acmePod) {
		t.Fatal("a beta Job accepts a node running an acme Job")
	}
	if teamRepels(t, acme, teamJob(t, "acme").Spec.Template.Labels) {
		t.Fatal("an acme Job refuses a node running another acme Job, so a team's Jobs never pack")
	}
	// A node's daemonset and system pods carry no team label.
	if teamRepels(t, acme, map[string]string{"app": "kube-proxy"}) {
		t.Fatal("an acme Job refuses a node running an unlabeled pod, so it can schedule nowhere")
	}
}

// The negative control: a Job whose anti-affinity term is gone must fail the
// check the tests above rely on.
func TestTeamRepels_FailsWithoutTheTerm(t *testing.T) {
	acme, beta := teamJob(t, "acme"), teamJob(t, "beta")
	acme.Spec.Template.Spec.Affinity = nil
	if teamRepels(t, acme, beta.Spec.Template.Labels) {
		t.Fatal("a Job with no anti-affinity still reads as repelling another team")
	}
	got, _ := yaml.Marshal(acme.Spec.Template.Spec.Affinity)
	if string(got) == wantTeamAffinity {
		t.Fatal("the rendered-affinity check passes a Job with no anti-affinity")
	}
}

func TestTeamLabelValue(t *testing.T) {
	long := strings.Repeat("a", 64)
	cases := []struct {
		team string
		want string
	}{
		{team: "acme", want: "acme"},
		{team: "team-42", want: "team-42"},
		{team: "", want: "default"},
		{team: "Acme"},
		{team: "acme_corp"},
		{team: "-acme"},
		{team: long},
		{team: "sha256-deadbeef"},
	}
	for _, tc := range cases {
		got := TeamLabelValue(tc.team)
		if errs := validation.IsValidLabelValue(got); len(errs) > 0 {
			t.Errorf("TeamLabelValue(%q) = %q, not a label value: %v", tc.team, got, errs)
		}
		if got != TeamLabelValue(tc.team) {
			t.Errorf("TeamLabelValue(%q) is not deterministic", tc.team)
		}
		switch {
		case tc.want != "":
			if got != tc.want {
				t.Errorf("TeamLabelValue(%q) = %q, want %q", tc.team, got, tc.want)
			}
		default:
			if !strings.HasPrefix(got, hashedTeamPrefix) || len(got) != len(hashedTeamPrefix)+40 {
				t.Errorf("TeamLabelValue(%q) = %q, want a hashed value", tc.team, got)
			}
		}
	}
	if TeamLabelValue("Acme") == TeamLabelValue("acme") {
		t.Error("two different teams share a label value")
	}
}
