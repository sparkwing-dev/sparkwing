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

// teamJob renders an off-band class, where the team term is the only
// anti-affinity; a band Job also refuses every other Job's node.
func teamJob(t *testing.T, team string) *batchv1.Job {
	t.Helper()
	return classJob(t, Config{Image: "img", Team: team}, 2)
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

func TestBuildJob_BandJobRefusesEveryOtherJobsNode(t *testing.T) {
	band := classJob(t, Config{Image: "img", Team: "acme"}, 4)
	if !teamRepels(t, band, classJob(t, Config{Image: "img", Team: "acme"}, 4).Spec.Template.Labels) {
		t.Fatal("a band Job accepts a node running another Job of its own team, so two classes share a machine")
	}
	if teamRepels(t, band, map[string]string{"app": "kube-proxy"}) {
		t.Fatal("a band Job refuses a node running a daemonset pod, so it can schedule nowhere")
	}
}

// The negative control: with only the team term, a band Job packs beside its
// own team's Jobs, which the test above must catch.
func TestBandRepels_FailsWithoutTheOneJobTerm(t *testing.T) {
	band := classJob(t, Config{Image: "img", Team: "acme"}, 4)
	band.Spec.Template.Spec.Affinity = teamAntiAffinity("acme")
	if teamRepels(t, band, classJob(t, Config{Image: "img", Team: "acme"}, 4).Spec.Template.Labels) {
		t.Fatal("a band Job with only the team term still reads as refusing its own team's node")
	}
}

// teamNode is the label set a band pool node carries after Karpenter boots it
// for a Job of team.
func teamNode(team string) labels.Set {
	return labels.Set{cpuBandKey: cpuBandSmall, TeamNodeLabel: TeamLabelValue(team)}
}

func selects(job *batchv1.Job, node labels.Set) bool {
	return labels.SelectorFromSet(job.Spec.Template.Spec.NodeSelector).Matches(node)
}

func TestBuildJob_BandNodeServesOnlyTheTeamThatBootedIt(t *testing.T) {
	for _, team := range []string{"acme", "Acme Corp", ""} {
		job := classJob(t, Config{Image: "img", Team: team}, 4)
		if !selects(job, teamNode(team)) {
			t.Fatalf("team %q: a band Job refuses a node its own team booted, so a DAG never reuses one", team)
		}
		if selects(job, teamNode("beta")) {
			t.Fatalf("team %q: a band Job selects a node beta booted", team)
		}
		if selects(job, labels.Set{cpuBandKey: cpuBandSmall}) {
			t.Fatalf("team %q: a band Job selects a node with no team, which the next team could reuse", team)
		}
		if errs := validation.IsValidLabelValue(job.Spec.Template.Spec.NodeSelector[TeamNodeLabel]); len(errs) > 0 {
			t.Fatalf("team %q: node label value is invalid: %v", team, errs)
		}
	}
}

func TestBuildJob_TeamOutranksTheOperatorOnTheTeamNodeKey(t *testing.T) {
	cfg := Config{Image: "img", Team: "acme", NodeSelector: map[string]string{TeamNodeLabel: "beta"}}
	job := classJob(t, cfg, 4)
	if got := job.Spec.Template.Spec.NodeSelector[TeamNodeLabel]; got != "acme" {
		t.Fatalf("team node = %q, want acme over the operator's value", got)
	}
	if cfg.NodeSelector[TeamNodeLabel] != "beta" {
		t.Fatalf("buildJob wrote back into the operator's selector: %v", cfg.NodeSelector)
	}
}

func TestBuildJob_OffBandJobNamesNoTeamNode(t *testing.T) {
	if got, ok := teamJob(t, "acme").Spec.Template.Spec.NodeSelector[TeamNodeLabel]; ok {
		t.Fatalf("an off-band Job selects team node %q, which no fixed node carries", got)
	}
}

// The negative control: without the team key a band Job selects another
// team's node, which the test above must catch.
func TestBandTeamNode_FailsWithoutTheKey(t *testing.T) {
	job := classJob(t, Config{Image: "img", Team: "acme"}, 4)
	delete(job.Spec.Template.Spec.NodeSelector, TeamNodeLabel)
	if !selects(job, teamNode("beta")) {
		t.Fatal("a band Job without the team key still refuses beta's node")
	}
}

func TestBuildJob_UnbilledBandJobStillSelectsItsTeamsNode(t *testing.T) {
	job := classJob(t, Config{Image: "img", Team: "acme", NodeSelector: map[string]string{cpuBandKey: cpuBandSmall}}, 0)
	if !selects(job, teamNode("acme")) || selects(job, teamNode("beta")) {
		t.Fatalf("an unbilled band Job selects %v, want only acme's node", job.Spec.Template.Spec.NodeSelector)
	}
}
