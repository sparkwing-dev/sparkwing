package teamblob

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Tally is what one namespace holds.
type Tally struct {
	Bytes   int64 `json:"bytes"`
	Objects int64 `json:"objects"`
}

// Measurement is what a whole-store listing found.
type Measurement struct {
	// Teams is each team namespace's holdings after expiry.
	Teams map[string]Tally
	// Operator is the store root outside every team's namespace.
	Operator Tally
	// Expired is what the listing deleted past a team's maximum age.
	Expired Tally
	At      time.Time
}

// Measure lists every namespace in the store once, one LIST per thousand
// objects plus a delimited listing of the root and of teams/, and deletes
// team objects past [Options.TeamObjectMaxAge] in the same walk. It runs on
// a schedule of hours, never per request. A namespace whose listing fails is
// left out and the error returned, so a caller never takes a partial
// measurement for the whole.
func (s *Store) Measure(ctx context.Context) (Measurement, error) {
	out := Measurement{Teams: map[string]Tally{}, At: s.now().UTC()}
	root := s.root()
	tops, err := s.children(ctx, root)
	if err != nil {
		return out, err
	}
	var errs []error
	for _, top := range tops {
		name := strings.TrimSuffix(strings.TrimPrefix(top, root), "/")
		switch name {
		case metaSegment:
			continue
		case teamsSegment:
			teams, err := s.children(ctx, top)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			for _, tp := range teams {
				team := strings.TrimSuffix(strings.TrimPrefix(tp, top), "/")
				if !ValidTeam(team) {
					continue
				}
				t, expired, err := s.measureTeam(ctx, team, tp)
				out.Expired.Bytes += expired.Bytes
				out.Expired.Objects += expired.Objects
				if err != nil {
					errs = append(errs, err)
					continue
				}
				out.Teams[team] = t
			}
		default:
			t, err := s.measure(ctx, top)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			out.Operator.Bytes += t.Bytes
			out.Operator.Objects += t.Objects
		}
	}
	return out, errors.Join(errs...)
}

func (s *Store) measure(ctx context.Context, prefix string) (Tally, error) {
	var t Tally
	err := s.walk(ctx, prefix, func(o types.Object) {
		t.Bytes += aws.ToInt64(o.Size)
		t.Objects++
	})
	if err != nil {
		return Tally{}, err
	}
	return t, nil
}

// safety: only deletions the store confirmed leave the tally; an object it refused stays in it.
func (s *Store) measureTeam(ctx context.Context, team, prefix string) (Tally, Tally, error) {
	var t Tally
	var expired []sizedKey
	var age time.Duration
	if s.maxAge != nil {
		age = s.maxAge(team)
	}
	cutoff := s.now().Add(-age)
	err := s.walk(ctx, prefix, func(o types.Object) {
		t.Bytes += aws.ToInt64(o.Size)
		t.Objects++
		if age > 0 && aws.ToTime(o.LastModified).Before(cutoff) {
			expired = append(expired, sizedKey{key: aws.ToString(o.Key), size: aws.ToInt64(o.Size)})
		}
	})
	if err != nil {
		return Tally{}, Tally{}, err
	}
	d, err := s.deleteKeys(ctx, expired)
	t.Bytes -= d.Bytes
	t.Objects -= d.Objects
	return t, Tally{Bytes: d.Bytes, Objects: d.Objects}, err
}

func (s *Store) children(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	var token *string
	for page := 0; ; page++ {
		if page >= s.maxPages {
			return out, ErrListTruncated
		}
		var res *s3.ListObjectsV2Output
		err := s.guarded(ctx, func() error {
			var lerr error
			res, lerr = s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
				Bucket:            aws.String(s.bucket),
				Prefix:            aws.String(prefix),
				Delimiter:         aws.String("/"),
				ContinuationToken: token,
			})
			return lerr
		})
		if err != nil {
			return nil, fmt.Errorf("teamblob: list %s: %w", prefix, err)
		}
		for _, p := range res.CommonPrefixes {
			out = append(out, aws.ToString(p.Prefix))
		}
		if !aws.ToBool(res.IsTruncated) {
			return out, nil
		}
		token = res.NextContinuationToken
	}
}

// ListDirs returns the names of the prefixes directly under team's
// relPrefix, one delimited LIST per thousand of them. An index keyed by
// date uses it to find its days without listing their entries.
func (s *Store) ListDirs(ctx context.Context, team, relPrefix string) ([]string, error) {
	full, _, err := s.listPrefix(team, relPrefix)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(full, "/") {
		full += "/"
	}
	prefixes, err := s.children(ctx, full)
	names := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		names = append(names, strings.TrimSuffix(strings.TrimPrefix(p, full), "/"))
	}
	return names, err
}
