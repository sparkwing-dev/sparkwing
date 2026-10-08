package logseg

import (
	"fmt"
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
