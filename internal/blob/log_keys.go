package blob

import (
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"unicode"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

const (
	segmentPrefix = "seg-"
	segmentSuffix = ".ndjson.gz"
)

// logClassPrefix locates every log object with one retention class.
func logClassPrefix(class string) string { return "logs/" + class + "/" }

// logRunPrefix validates the tenant and run namespace for physical logs.
func logRunPrefix(class, team, runID string) (string, error) {
	if _, ok := LogClassDays(class); !ok {
		return "", fmt.Errorf("logseg: retention class %q is not r<days>", class)
	}
	if team != "_" && !ValidLogTeam(team) {
		return "", fmt.Errorf("logseg: team %q is not a team slug", team)
	}
	if err := validLogPath(runID, false); err != nil {
		return "", err
	}
	return logClassPrefix(class) + "teams/" + team + "/runs/" + runID + "/", nil
}

// LogSubjectPrefix derives a distinct namespace for each tagged subject and fence.
func LogSubjectPrefix(class string, subject LogSubject) (string, error) {
	if err := subject.Validate(); err != nil {
		return "", err
	}
	run, err := logRunPrefix(class, subject.Team, subject.RunID)
	if err != nil {
		return "", err
	}
	if a := subject.Attempt; a != nil {
		if err := validLogPath(a.NodeID, true); err != nil {
			return "", err
		}
		return fmt.Sprintf("%sjobs/%s/a%d-g%d/", run, escapeLogNode(a.NodeID), a.Attempt, a.Generation), nil
	}
	u := subject.Unit
	if u.Kind == LogEval {
		if err := validLogPath(u.Source.NodeID, true); err != nil {
			return "", err
		}
		return fmt.Sprintf("%sunits/eval/%s/s%d-g%d/", run, escapeLogNode(u.Source.NodeID), u.Source.Generation, u.Generation), nil
	}
	return fmt.Sprintf("%sunits/%s/g%d/", run, u.Kind, u.Generation), nil
}

func escapeLogNode(nodeID string) string {
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

// LogSegmentKey names a positive segment slot within a subject prefix.
func LogSegmentKey(prefix string, n int) string {
	return fmt.Sprintf("%s%s%06d%s", prefix, segmentPrefix, n, segmentSuffix)
}

// LogManifestKey names the final manifest within a subject prefix.
func LogManifestKey(prefix string) string { return prefix + "manifest.json" }

// LogSegmentNumber recognizes canonical positive segment slots under a prefix.
func LogSegmentNumber(prefix, key string) (int, bool) {
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
	if err != nil || n < 1 || n > limits.MaxLogSegmentNumber || fmt.Sprintf("%06d", n) != digits {
		return 0, false
	}
	return n, true
}

// LogObjectKey binds zero to the final manifest and positive indexes to immutable segments.
func LogObjectKey(subject LogSubject, class string, index int64) (string, error) {
	prefix, err := LogSubjectPrefix(class, subject)
	if err != nil {
		return "", err
	}
	if index < 0 || index > int64(limits.MaxLogSegmentNumber) {
		return "", fmt.Errorf("%w: invalid log signing slot", ErrIntegrity)
	}
	if index == 0 {
		return LogManifestKey(prefix), nil
	}
	return LogSegmentKey(prefix, int(index)), nil
}

func validLogPath(id string, hierarchical bool) error {
	if !fs.ValidPath(id) || id == "." || (!hierarchical && strings.ContainsRune(id, '/')) || strings.ContainsRune(id, '\\') || strings.ContainsFunc(id, unicode.IsControl) {
		return fmt.Errorf("invalid log identifier %q", id)
	}
	return nil
}

// ValidLogTeam accepts a DNS-label tenant for log storage.
func ValidLogTeam(team string) bool {
	if team == "" || len(team) > 63 || team[0] == '-' || team[len(team)-1] == '-' {
		return false
	}
	for _, c := range team {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}
