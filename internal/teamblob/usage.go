package teamblob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// TeamUsage is what one team holds in the store. The operator's own
// objects are reported under the empty team.
type TeamUsage struct {
	Bytes   int64 `json:"bytes"`
	Objects int64 `json:"objects"`
	// ReconciledAt is when a listing last replaced the running count;
	// zero means the count has only ever been kept by writes.
	ReconciledAt time.Time `json:"reconciled_at,omitzero"`
}

// Usage is the store's running count per team.
type Usage struct {
	mu           sync.Mutex
	teams        map[string]TeamUsage
	dirty        bool
	reconciledAt time.Time
}

// ReconciledAt is when a whole-store reconcile last finished cleanly.
func (u *Usage) ReconciledAt() time.Time {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.reconciledAt
}

func newUsage() *Usage { return &Usage{teams: map[string]TeamUsage{}} }

// safety: a delete of an object the count never saw, such as one written
// before the count existed, must not drive a team below zero and hand it
// allowance it does not have; the next reconcile restores the truth.
func (u *Usage) add(team string, bytes, objects int64) {
	if bytes == 0 && objects == 0 {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	t := u.teams[team]
	t.Bytes = max(t.Bytes+bytes, 0)
	t.Objects = max(t.Objects+objects, 0)
	u.teams[team] = t
	u.dirty = true
}

func (u *Usage) forget(team string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.teams, team)
	u.dirty = true
}

func (u *Usage) set(team string, t TeamUsage) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if t.Bytes == 0 && t.Objects == 0 {
		delete(u.teams, team)
	} else {
		u.teams[team] = t
	}
	u.dirty = true
}

// Team reports one team's count.
func (u *Usage) Team(team string) TeamUsage {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.teams[team]
}

// Snapshot copies every team's count.
func (u *Usage) Snapshot() map[string]TeamUsage {
	u.mu.Lock()
	defer u.mu.Unlock()
	return maps.Clone(u.teams)
}

// Total sums every team's count, the operator's included.
func (u *Usage) Total() (bytes, objects int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, t := range u.teams {
		bytes += t.Bytes
		objects += t.Objects
	}
	return bytes, objects
}

// Usage is the store's running count.
func (s *Store) Usage() *Usage { return s.usage }

// ReconcileTeam lists team's namespace and replaces its running count
// with the total. It costs one LIST per thousand of the team's objects.
func (s *Store) ReconcileTeam(ctx context.Context, team string) (TeamUsage, error) {
	if team == "" {
		return TeamUsage{}, fmt.Errorf("%w: the operator namespace is reconciled by Reconcile", ErrInvalidKey)
	}
	ns, err := s.namespace(team)
	if err != nil {
		return TeamUsage{}, err
	}
	t, err := s.measure(ctx, ns)
	if err != nil {
		return TeamUsage{}, err
	}
	s.usage.set(team, t)
	return t, nil
}

// Reconcile measures every namespace in the store and replaces the whole
// running count. It lists every object once, one LIST per thousand, plus
// a delimited listing of the root and of teams/ to find the namespaces,
// so it runs on a schedule of hours, never per request. A namespace whose
// listing fails keeps its running count and the error is returned.
func (s *Store) Reconcile(ctx context.Context) error {
	root := s.root()
	tops, err := s.children(ctx, root)
	if err != nil {
		return err
	}
	var operator TeamUsage
	var errs []error
	seen := map[string]bool{}
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
				seen[team] = true
				t, err := s.measureTeam(ctx, team, tp)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				s.usage.set(team, t)
			}
		default:
			t, err := s.measure(ctx, top)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			operator.Bytes += t.Bytes
			operator.Objects += t.Objects
		}
	}
	operator.ReconciledAt = s.now().UTC()
	if len(errs) == 0 {
		s.usage.set("", operator)
		s.usage.mu.Lock()
		s.usage.reconciledAt = operator.ReconciledAt
		s.usage.mu.Unlock()
		// safety: a team the count remembers but the bucket no longer holds
		// was purged by some other path; keeping it would charge it forever.
		for team := range s.usage.Snapshot() {
			if team != "" && !seen[team] {
				s.usage.forget(team)
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Store) measure(ctx context.Context, prefix string) (TeamUsage, error) {
	var t TeamUsage
	err := s.walk(ctx, prefix, func(o types.Object) {
		t.Bytes += aws.ToInt64(o.Size)
		t.Objects++
	})
	if err != nil {
		return TeamUsage{}, err
	}
	t.ReconciledAt = s.now().UTC()
	return t, nil
}

// measureTeam measures one team's namespace and, with a maximum age set,
// deletes the objects older than it in the same listing. What the store
// confirmed deleted leaves the count; an object it refused stays counted.
func (s *Store) measureTeam(ctx context.Context, team, prefix string) (TeamUsage, error) {
	var t TeamUsage
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
		return TeamUsage{}, err
	}
	d, err := s.deleteKeys(ctx, expired)
	t.Bytes -= d.Bytes
	t.Objects -= d.Objects
	t.ReconciledAt = s.now().UTC()
	return t, err
}

// children lists the next level of prefixes under prefix.
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

type usageFile struct {
	SavedAt      time.Time            `json:"saved_at"`
	ReconciledAt time.Time            `json:"reconciled_at,omitzero"`
	Teams        map[string]TeamUsage `json:"teams"`
}

// SaveUsage writes the running count to the store's metadata object when
// it changed since the last save. It is one PUT, so a caller saves on a
// timer of minutes and at shutdown.
func (s *Store) SaveUsage(ctx context.Context) error {
	s.usage.mu.Lock()
	if !s.usage.dirty {
		s.usage.mu.Unlock()
		return nil
	}
	body, err := json.Marshal(usageFile{SavedAt: s.now().UTC(), ReconciledAt: s.usage.reconciledAt, Teams: maps.Clone(s.usage.teams)})
	s.usage.dirty = false
	s.usage.mu.Unlock()
	if err != nil {
		return err
	}
	if err := s.guarded(ctx, func() error {
		_, perr := s.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:        aws.String(s.bucket),
			Key:           aws.String(s.root() + usageRel),
			Body:          bytes.NewReader(body),
			ContentLength: aws.Int64(int64(len(body))),
			ContentType:   aws.String("application/json"),
		})
		return perr
	}); err != nil {
		s.usage.mu.Lock()
		s.usage.dirty = true
		s.usage.mu.Unlock()
		return fmt.Errorf("teamblob: save usage: %w", err)
	}
	return nil
}

// LoadUsage restores the running count a previous process saved. It
// reports false when there is nothing saved, which is when a caller
// reconciles instead.
func (s *Store) LoadUsage(ctx context.Context) (time.Time, bool, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.root() + usageRel),
	})
	if err != nil {
		if isNotFound(err) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("teamblob: load usage: %w", err)
	}
	defer out.Body.Close()
	var f usageFile
	if err := json.NewDecoder(out.Body).Decode(&f); err != nil {
		return time.Time{}, false, fmt.Errorf("teamblob: load usage: %w", err)
	}
	s.usage.mu.Lock()
	s.usage.teams = map[string]TeamUsage{}
	for team, t := range f.Teams {
		if team == "" || ValidTeam(team) {
			s.usage.teams[team] = t
		}
	}
	s.usage.reconciledAt = f.ReconciledAt
	s.usage.dirty = false
	s.usage.mu.Unlock()
	return f.SavedAt, true, nil
}

// Maintain restores the saved count at start, reconciles when nothing
// was saved or the last reconcile is older than every, saves the count
// every saveEvery while it changes, and reconciles every every. It
// returns when ctx is done, saving once more on the way out. Errors go
// to report and never stop the loop.
func (s *Store) Maintain(ctx context.Context, every, saveEvery time.Duration, report func(op string, err error)) {
	if report == nil {
		report = func(string, error) {}
	}
	_, loaded, err := s.LoadUsage(ctx)
	if err != nil {
		report("load usage", err)
	}
	last := s.usage.ReconciledAt()
	if !loaded || (every > 0 && s.now().Sub(last) >= every) {
		if err := s.Reconcile(ctx); err != nil {
			report("reconcile usage", err)
		}
		last = s.now()
	}
	if saveEvery <= 0 {
		saveEvery = 5 * time.Minute
	}
	save := time.NewTicker(saveEvery)
	defer save.Stop()
	for {
		select {
		case <-ctx.Done():
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			if err := s.SaveUsage(sctx); err != nil {
				report("save usage", err)
			}
			cancel()
			return
		case <-save.C:
			if every > 0 && s.now().Sub(last) >= every {
				if err := s.Reconcile(ctx); err != nil {
					report("reconcile usage", err)
				}
				last = s.now()
			}
			if err := s.SaveUsage(ctx); err != nil {
				report("save usage", err)
			}
		}
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
