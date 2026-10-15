// Package teamblob measures tenant bucket namespaces and constructs scoped keys.
package teamblob

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Client provides only the listing operation required for measurement.
type Client interface {
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// ErrInvalidKey reports an invalid tenant or relative key.
var ErrInvalidKey = errors.New("teamblob: invalid key")

// ErrListTruncated reports an incomplete listing at its page budget.
var ErrListTruncated = errors.New("teamblob: listing stopped at its page cap")

const (
	// DefaultMaxListPages bounds each listing walk.
	DefaultMaxListPages = 1000
	teamsSegment        = "teams"
	metaSegment         = "_meta"
)

// Options configures bucket measurement and scoped key construction.
type Options struct {
	Bucket string
	Prefix string
	Client Client
	// MaxListPages defaults to DefaultMaxListPages.
	MaxListPages int
	// Now defaults to time.Now.
	Now func() time.Time
}

// Store measures a bucket prefix without modifying its objects.
type Store struct {
	bucket   string
	prefix   string
	client   Client
	maxPages int
	now      func() time.Time
}

// New validates the bucket and namespace before constructing a measurement store.
func New(opts Options) (*Store, error) {
	if opts.Bucket == "" || opts.Client == nil || strings.Trim(opts.Prefix, "/") == "" {
		return nil, errors.New("teamblob: bucket, client and nonempty prefix are required")
	}
	if opts.MaxListPages <= 0 {
		opts.MaxListPages = DefaultMaxListPages
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Store{bucket: opts.Bucket, prefix: strings.Trim(opts.Prefix, "/"), client: opts.Client, maxPages: opts.MaxListPages, now: opts.Now}, nil
}

// Bucket returns the bucket used for measurement and signing.
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
