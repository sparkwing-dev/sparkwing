// Package teamblob constructs tenant-scoped object keys.
package teamblob

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidKey reports an invalid tenant or relative key.
var ErrInvalidKey = errors.New("teamblob: invalid key")

const (
	teamsSegment = "teams"
	metaSegment  = "_meta"
)

// Options configures the bucket and key namespace.
type Options struct {
	Bucket string
	Prefix string
}

// Store constructs scoped keys for its configured bucket.
type Store struct {
	bucket string
	prefix string
}

// New validates the bucket and namespace before constructing scoped keys.
func New(opts Options) (*Store, error) {
	if opts.Bucket == "" || strings.Trim(opts.Prefix, "/") == "" {
		return nil, errors.New("teamblob: bucket and nonempty prefix are required")
	}
	return &Store{bucket: opts.Bucket, prefix: strings.Trim(opts.Prefix, "/")}, nil
}

// Bucket returns the configured bucket.
func (s *Store) Bucket() string { return s.bucket }

// ValidTeam reports whether a team is a DNS-label slug.
func ValidTeam(team string) bool {
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

func validRel(rel string) bool {
	if rel == "" || len(rel) > 900 || strings.HasPrefix(rel, "/") {
		return false
	}
	for seg := range strings.SplitSeq(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	for _, r := range rel {
		if r == '\\' || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func (s *Store) root() string { return s.prefix + "/" }

// Key returns the object key inside the selected tenant or operator namespace.
func (s *Store) Key(team, rel string) (string, error) {
	if !validRel(rel) {
		return "", fmt.Errorf("%w: %q", ErrInvalidKey, rel)
	}
	if team == "" {
		first, _, _ := strings.Cut(rel, "/")
		if first == teamsSegment || first == metaSegment {
			return "", fmt.Errorf("%w: %q is reserved", ErrInvalidKey, rel)
		}
		return s.root() + rel, nil
	}
	if !ValidTeam(team) {
		return "", fmt.Errorf("%w: team %q is not a slug", ErrInvalidKey, team)
	}
	return s.root() + teamsSegment + "/" + team + "/" + rel, nil
}
