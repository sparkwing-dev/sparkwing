package k8s

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TeamLabel carries the owning team on every Job and pod the runner creates.
// A node that runs one team's pod refuses every other team's pod, because a
// node also hosts ambient state, such as a shared Docker daemon, that team code
// can reach.
const TeamLabel = "sparkwing.dev/team"

var teamLabelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// hashedTeamPrefix marks a label value derived by hashing. A team that is
// already a slug keeps its name unless it starts with this prefix, so a
// verbatim value never equals a hashed one.
const hashedTeamPrefix = "sha256-"

// TeamLabelValue returns the label value for team. A DNS-label slug (lowercase
// letters, digits and inner hyphens, at most 63 characters) is used as is, so
// the label reads the same as the team. Anything else becomes "sha256-" and
// the first 40 hex digits of the SHA-256 of the team name, which is
// deterministic, fits the 63-character limit, and keeps distinct teams
// distinct. An empty team is [store.DefaultTeam], the team that owns every row
// written without one.
func TeamLabelValue(team string) string {
	if team == "" {
		team = string(store.DefaultTeam)
	}
	if teamLabelPattern.MatchString(team) && !strings.HasPrefix(team, hashedTeamPrefix) {
		return team
	}
	sum := sha256.Sum256([]byte(team))
	return hashedTeamPrefix + hex.EncodeToString(sum[:])[:40]
}

// teamAntiAffinity keeps a pod labeled with team value off any node that runs
// a pod of another team. Same-team pods still share nodes.
func teamAntiAffinity(value string) *corev1.Affinity {
	return &corev1.Affinity{
		PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
				LabelSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						// safety: NotIn also matches a pod without the key, so
						// without Exists every daemonset pod would repel the Job.
						{Key: TeamLabel, Operator: metav1.LabelSelectorOpExists},
						{Key: TeamLabel, Operator: metav1.LabelSelectorOpNotIn, Values: []string{value}},
					},
				},
				// safety: another team's runner may create its Jobs in another
				// namespace, and a term with no selector sees only its own.
				NamespaceSelector: &metav1.LabelSelector{},
				TopologyKey:       corev1.LabelHostname,
			}},
		},
	}
}
