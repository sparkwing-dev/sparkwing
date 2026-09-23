package storagequota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Headers a controller's claim check answers the logs service with, naming
// the run's team's tier and the allowance its shares are cut from.
const (
	TierHeader      = "X-Sparkwing-Storage-Tier"
	AllowanceHeader = "X-Sparkwing-Storage-Allowance"
)

// DefaultStandingTTL is how long a looked-up standing is trusted before the
// next write asks again.
const DefaultStandingTTL = time.Minute

// ErrPaused is the class of a refusal of a team with neither credits nor a
// free-tier slot.
var ErrPaused = errors.New("free storage is paused; buy credits or join the waitlist")

// Standing is what a team may store.
type Standing struct {
	Tier           Tier  `json:"tier"`
	AllowanceBytes int64 `json:"allowance_bytes"`
}

// freeDefault is the standing of a team never looked up, or looked up only
// while the lookup failed: held to the default allowance, never lifted.
var freeDefault = Standing{Tier: TierFree, AllowanceBytes: DefaultAllowanceBytes}

// Lookup fetches a team's standing from the controller.
type Lookup func(ctx context.Context, team string) (Standing, error)

// Options configures a [Quota].
type Options struct {
	// Share cuts this store's part out of a team's allowance.
	Share func(allowance int64) int64
	// Used reports what team holds in this store now. It is called with
	// the quota's lock held and must not call back into the quota.
	Used func(team string) int64
	// Lookup fetches standings. Nil takes them only from [Quota.Observe].
	Lookup Lookup
	// Exempt reports a team no limit applies to, the operator's own.
	Exempt func(team string) bool
	// TTL defaults to [DefaultStandingTTL].
	TTL time.Duration
	Now func() time.Time
}

// Quota admits or refuses a team's writes to one store against its share.
// It holds, under one lock, the bytes every write in flight has reserved
// and the standings it has learned, so two concurrent writes see each
// other's reservations.
type Quota struct {
	opts Options

	mu        sync.Mutex
	inflight  map[string]int64
	standings map[string]cachedStanding
}

type cachedStanding struct {
	Standing
	at time.Time
}

// New returns a quota over opts.
func New(opts Options) *Quota {
	if opts.TTL <= 0 {
		opts.TTL = DefaultStandingTTL
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Exempt == nil {
		opts.Exempt = func(string) bool { return false }
	}
	return &Quota{opts: opts, inflight: map[string]int64{}, standings: map[string]cachedStanding{}}
}

// Error refuses a write past a team's share, or any write of a team with no
// slot.
type Error struct {
	Team      string
	Used      int64
	Allowed   int64
	Requested int64
	Paused    bool
}

func (e *Error) Error() string {
	if e.Paused {
		return fmt.Sprintf("%s: team %s has no credits and holds no free-tier slot", ErrPaused, e.Team)
	}
	return fmt.Sprintf("free storage allowance exceeded: team %s holds %d of the %d bytes its free share allows here "+
		"and this write needs %d; add credits to store more", e.Team, e.Used, e.Allowed, e.Requested)
}

// Unwrap reports [ErrPaused] for a paused refusal.
func (e *Error) Unwrap() error {
	if e.Paused {
		return ErrPaused
	}
	return nil
}

// Observe records a standing learned from another answer, such as a claim
// check the controller made anyway.
func (q *Quota) Observe(team string, s Standing) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.standings[team] = cachedStanding{Standing: s, at: q.opts.Now()}
}

// safety: a lookup that fails keeps the last answer, and a team never
// answered for is free, so an outage of the controller never lifts a limit.
func (q *Quota) standing(ctx context.Context, team string) Standing {
	q.mu.Lock()
	cached, ok := q.standings[team]
	q.mu.Unlock()
	if ok && (q.opts.Lookup == nil || q.opts.Now().Sub(cached.at) < q.opts.TTL) {
		return cached.Standing
	}
	if q.opts.Lookup == nil {
		return freeDefault
	}
	got, err := q.opts.Lookup(ctx, team)
	if err != nil {
		if ok {
			return cached.Standing
		}
		return freeDefault
	}
	q.Observe(team, got)
	return got
}

// Reserve admits n bytes for team or refuses them. The caller writes, then
// calls release whatever the write did; the store's own count carries what
// was written from then on.
func (q *Quota) Reserve(ctx context.Context, team string, n int64) (release func(), err error) {
	_, release, err = q.reserve(ctx, team, max(n, 0), true)
	return release, err
}

// ReserveUpTo admits as many of limit bytes as team has room for, at least
// one, for a write whose size is not known before it is read. The caller
// cuts the write at the granted bytes. A limit of zero or less asks for all
// the room there is.
func (q *Quota) ReserveUpTo(ctx context.Context, team string, limit int64) (granted int64, release func(), err error) {
	return q.reserve(ctx, team, limit, false)
}

func noop() {}

func (q *Quota) reserve(ctx context.Context, team string, n int64, exact bool) (int64, func(), error) {
	if q.opts.Exempt(team) {
		return unlimited(n), noop, nil
	}
	s := q.standing(ctx, team)
	switch s.Tier {
	case TierFunded:
		return unlimited(n), noop, nil
	case TierNone:
		return 0, nil, &Error{Team: team, Requested: n, Paused: true}
	}
	share := q.opts.Share(s.AllowanceBytes)
	q.mu.Lock()
	used := q.opts.Used(team) + q.inflight[team]
	room := share - used
	want := n
	if !exact && (n <= 0 || n > room) {
		want = room
	}
	if want > room || room <= 0 {
		q.mu.Unlock()
		return 0, nil, &Error{Team: team, Used: used, Allowed: share, Requested: max(n, 1)}
	}
	q.inflight[team] += want
	q.mu.Unlock()
	return want, sync.OnceFunc(func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.inflight[team] -= want; q.inflight[team] <= 0 {
			delete(q.inflight, team)
		}
	}), nil
}

func unlimited(n int64) int64 {
	if n <= 0 {
		return 1<<63 - 1
	}
	return n
}

// FreeUsedBytes sums what every team last known to be free holds here,
// with its writes in flight: the free bytes this store answers for.
func (q *Quota) FreeUsedBytes() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	var total int64
	for team, s := range q.standings {
		if s.Tier == TierFree {
			total += q.opts.Used(team) + q.inflight[team]
		}
	}
	return total
}

// RegisterMetric exports [Quota.FreeUsedBytes] as
// sparkwing.free_storage.used_bytes{store=name}.
func RegisterMetric(meter metric.Meter, name string, q *Quota) error {
	g, err := meter.Int64ObservableGauge("sparkwing.free_storage.used_bytes",
		metric.WithDescription("Bytes teams without credits hold in this store, writes in flight included"))
	if err != nil {
		return err
	}
	attrs := metric.WithAttributes(attribute.String("store", name))
	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(g, q.FreeUsedBytes(), attrs)
		return nil
	}, g)
	return err
}

// HTTPLookup asks controllerURL's GET /internal/teams/{team}/storage-tier
// with token as the bearer.
func HTTPLookup(controllerURL, token string, client *http.Client) Lookup {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	base := strings.TrimRight(controllerURL, "/")
	return func(ctx context.Context, team string) (Standing, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			base+"/internal/teams/"+url.PathEscape(team)+"/storage-tier", nil)
		if err != nil {
			return Standing{}, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		// #nosec G704 -- the origin is operator configuration and the team a checked slug
		resp, err := client.Do(req)
		if err != nil {
			return Standing{}, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			body, rerr := io.ReadAll(io.LimitReader(resp.Body, 512))
			if rerr != nil {
				return Standing{}, fmt.Errorf("storage tier of %s: controller answered %d: read body: %w", team, resp.StatusCode, rerr)
			}
			return Standing{}, fmt.Errorf("storage tier of %s: controller answered %d: %s",
				team, resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var out Standing
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&out); err != nil {
			return Standing{}, fmt.Errorf("storage tier of %s: %w", team, err)
		}
		switch out.Tier {
		case TierFunded, TierFree, TierNone:
			return out, nil
		}
		return Standing{}, fmt.Errorf("storage tier of %s: unknown tier %q", team, out.Tier)
	}
}
