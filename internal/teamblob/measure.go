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
	Teams map[string]Tally
	// Operator is the store root outside every team's namespace.
	Operator Tally
	At       time.Time
}

// Measure counts stored objects without changing them and reports incomplete listings as errors.
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
				t, err := s.measureNamespace(ctx, tp)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				out.Teams[team] = t
			}
		default:
			t, err := s.measureNamespace(ctx, top)
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

func (s *Store) measureNamespace(ctx context.Context, prefix string) (Tally, error) {
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

func (s *Store) children(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	var token *string
	for page := 0; ; page++ {
		if page >= s.maxPages {
			return out, ErrListTruncated
		}
		res, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(s.bucket), Prefix: aws.String(prefix),
			Delimiter: aws.String("/"), ContinuationToken: token,
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

func (s *Store) walk(ctx context.Context, prefix string, visit func(types.Object)) error {
	var token *string
	for page := 0; ; page++ {
		if page >= s.maxPages {
			return ErrListTruncated
		}
		out, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(s.bucket), Prefix: aws.String(prefix), ContinuationToken: token,
		})
		if err != nil {
			return fmt.Errorf("teamblob: list %s: %w", prefix, err)
		}
		for _, o := range out.Contents {
			visit(o)
		}
		if !aws.ToBool(out.IsTruncated) {
			return nil
		}
		token = out.NextContinuationToken
	}
}
