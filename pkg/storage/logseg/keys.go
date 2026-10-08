package logseg

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// Retention classes the controller assigns without operator
// configuration: 90 days for a team with credits or a single-team
// install, 30 days for a team without credits.
const (
	ClassStandard = "r90"
	ClassFree     = "r30"
)

// NoTeam is the team segment of a run that belongs to no team, so every
// log object sits under a team prefix.
const NoTeam = "_"

// ManifestName is the manifest's object name within an attempt prefix.
const ManifestName = "manifest.json"

const (
	segmentPrefix = "seg-"
	segmentSuffix = ".ndjson.gz"
)

// ClassDays returns the days a retention class keeps a log, and false
// for a name that is not a class.
func ClassDays(class string) (int, bool) {
	digits, ok := strings.CutPrefix(class, "r")
	if !ok || digits == "" || digits[0] == '0' {
		return 0, false
	}
	days, err := strconv.Atoi(digits)
	if err != nil || days < 1 {
		return 0, false
	}
	return days, true
}

// ClassPrefix is the key prefix every log of one retention class sits
// under, which the class's lifecycle rule names.
func ClassPrefix(class string) string { return "logs/" + class + "/" }

// RunPrefix is the key prefix under which every log of one run sits.
func RunPrefix(class, team, runID string) (string, error) {
	if _, ok := ClassDays(class); !ok {
		return "", fmt.Errorf("logseg: retention class %q is not r<days>", class)
	}
	if team != NoTeam && !teamblob.ValidTeam(team) {
		return "", fmt.Errorf("logseg: team %q is not a team slug", team)
	}
	if err := storage.SafeSegment(runID); err != nil {
		return "", err
	}
	return ClassPrefix(class) + "teams/" + team + "/runs/" + runID + "/", nil
}

// AttemptPrefix is the key prefix of one node attempt's segments and
// manifest. The node ID is escaped into one key segment: a hierarchical
// ID such as build/linux becomes build_2flinux, and no two node IDs
// share an escape.
func AttemptPrefix(class, team, runID, nodeID string, attempt, generation int64) (string, error) {
	run, err := RunPrefix(class, team, runID)
	if err != nil {
		return "", err
	}
	if err := storage.SafeRelPath(nodeID); err != nil {
		return "", err
	}
	if attempt < 1 || generation < 0 {
		return "", fmt.Errorf("logseg: attempt %d generation %d is not an execution attempt", attempt, generation)
	}
	return fmt.Sprintf("%s%s/a%d-g%d/", run, EscapeNode(nodeID), attempt, generation), nil
}

// EscapeNode turns a node ID into one key segment that
// storage.SafeArtifactKey accepts. Letters, digits, '-' and a '.' that
// does not lead pass through; every other byte becomes '_' and two hex
// digits, so the escape is reversible and '_' never stands for itself.
func EscapeNode(nodeID string) string {
	var b strings.Builder
	for i := 0; i < len(nodeID); i++ {
		c := nodeID[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '.' && i > 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "_%02x", c)
		}
	}
	return b.String()
}

// UnescapeNode reverses [EscapeNode], and reports false for a segment
// EscapeNode would not have written.
func UnescapeNode(escaped string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(escaped); i++ {
		if escaped[i] != '_' {
			b.WriteByte(escaped[i])
			continue
		}
		if i+2 >= len(escaped) {
			return "", false
		}
		c, err := strconv.ParseUint(escaped[i+1:i+3], 16, 8)
		if err != nil {
			return "", false
		}
		b.WriteByte(byte(c))
		i += 2
	}
	node := b.String()
	return node, node != "" && EscapeNode(node) == escaped
}

// Attempt names one attempt directory under a node's prefix.
type Attempt struct {
	Prefix     string
	NodeID     string
	Attempt    int64
	Generation int64
}

// FindAttempts lists every attempt of a run's logs under the given
// retention classes, for logs no run store records, such as those
// exported from the logs service. nodeID narrows the listing to one node
// when set. The newest attempt of each node comes last among that node's.
func FindAttempts(ctx context.Context, objs Objects, classes []string, team, runID, nodeID string) ([]Attempt, error) {
	seen := map[string]bool{}
	var out []Attempt
	for _, class := range classes {
		run, err := RunPrefix(class, team, runID)
		if err != nil {
			return nil, err
		}
		under := run
		if nodeID != "" {
			under += EscapeNode(nodeID) + "/"
		}
		keys, err := objs.List(ctx, under)
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			parts := strings.Split(strings.TrimPrefix(key, run), "/")
			if len(parts) != 3 {
				continue
			}
			node, ok := UnescapeNode(parts[0])
			var a Attempt
			if _, err := fmt.Sscanf(parts[1], "a%d-g%d", &a.Attempt, &a.Generation); !ok || err != nil ||
				fmt.Sprintf("a%d-g%d", a.Attempt, a.Generation) != parts[1] {
				continue
			}
			a.NodeID, a.Prefix = node, run+parts[0]+"/"+parts[1]+"/"
			if !seen[a.Prefix] {
				seen[a.Prefix] = true
				out = append(out, a)
			}
		}
	}
	slices.SortFunc(out, func(x, y Attempt) int {
		return cmp.Or(strings.Compare(x.NodeID, y.NodeID), cmp.Compare(x.Attempt, y.Attempt), cmp.Compare(x.Generation, y.Generation))
	})
	return out, nil
}

// SegmentKey is segment n's key within an attempt prefix. Numbers are
// six digits and dense from 1, so a listing sorts in write order.
func SegmentKey(prefix string, n int) string {
	return fmt.Sprintf("%s%s%06d%s", prefix, segmentPrefix, n, segmentSuffix)
}

// ManifestKey is the manifest's key within an attempt prefix.
func ManifestKey(prefix string) string { return prefix + ManifestName }

// SegmentNumber parses the number out of a key [SegmentKey] built under
// prefix, and reports false for any other key.
func SegmentNumber(prefix, key string) (int, bool) {
	name, ok := strings.CutPrefix(key, prefix)
	if !ok {
		return 0, false
	}
	digits, ok := strings.CutPrefix(name, segmentPrefix)
	if !ok {
		return 0, false
	}
	digits, ok = strings.CutSuffix(digits, segmentSuffix)
	if !ok || len(digits) < 6 {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}
