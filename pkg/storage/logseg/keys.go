package logseg

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
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

// ManifestName is the manifest's object name within a subject prefix.
const ManifestName = "manifest.json"

const (
	segmentPrefix = "seg-"
	segmentSuffix = ".ndjson.gz"
)

// ClassPrefix is the key prefix every log of one retention class sits
// under, which the class's lifecycle rule names.
func ClassPrefix(class string) string { return "logs/" + class + "/" }

// RunPrefix is the key prefix under which every log of one run sits.
func RunPrefix(class, team, runID string) (string, error) {
	if _, ok := blob.LogClassDays(class); !ok {
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

// SubjectPrefix separates job attempts from trigger, plan and eval units. Eval
// keys retain both their own lease generation and the immutable source fence.
func SubjectPrefix(class string, subject blob.LogSubject) (string, error) {
	if err := subject.Validate(); err != nil {
		return "", err
	}
	run, err := RunPrefix(class, subject.Team, subject.RunID)
	if err != nil {
		return "", err
	}
	if a := subject.Attempt; a != nil {
		if err := storage.SafeRelPath(a.NodeID); err != nil {
			return "", err
		}
		return fmt.Sprintf("%sjobs/%s/a%d-g%d/", run, EscapeNode(a.NodeID), a.Attempt, a.Generation), nil
	}
	u := subject.Unit
	if u.Kind == blob.LogEval {
		if err := storage.SafeRelPath(u.Source.NodeID); err != nil {
			return "", err
		}
		return fmt.Sprintf("%sunits/eval/%s/s%d-g%d/", run, EscapeNode(u.Source.NodeID), u.Source.Generation, u.Generation), nil
	}
	return fmt.Sprintf("%sunits/%s/g%d/", run, u.Kind, u.Generation), nil
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

// SegmentKey is segment n's key within a subject prefix. Numbers are
// six digits and dense from 1, so a listing sorts in write order.
func SegmentKey(prefix string, n int) string {
	return fmt.Sprintf("%s%s%06d%s", prefix, segmentPrefix, n, segmentSuffix)
}

// ManifestKey is the manifest's key within a subject prefix.
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
