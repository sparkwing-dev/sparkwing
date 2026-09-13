package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
)

// FloodPolicy bounds how many runs a burst of webhook deliveries or API
// submissions can create on one controller. The zero policy holds nothing
// back, which is the behavior of a controller that was never given one.
//
// A refused submission is always answered: the cap answers 429 and the
// queue-depth shed answers 503, both with Retry-After, and the controller logs
// the principal and the reason at warn. Nothing is dropped silently.
type FloodPolicy struct {
	// RunsPerPrincipalHour caps the runs one principal may create in a rolling
	// hour. Zero is unlimited. A webhook delivery counts against the
	// repository it names, because an unauthenticated delivery has no
	// principal of its own.
	RunsPerPrincipalHour int

	// ShedQueueDepth is the pending-trigger depth past which a new submission
	// is shed rather than queued. Zero never sheds.
	ShedQueueDepth int

	// DedupeWindow is how long a content-identical submission answers with the
	// run the first one started instead of starting a second. Zero dedupes
	// nothing. GitHub deliveries carry a delivery id and a body digest and are
	// deduped by the store regardless of this window.
	DedupeWindow time.Duration
}

// WithFloodPolicy installs p as this controller's trigger flood control.
// Calling it with the zero policy restores the unbounded behavior.
func (s *Server) WithFloodPolicy(p FloodPolicy) *Server {
	s.flood = newFloodControl(p)
	return s
}

// safety: a Retry-After shorter than the real refill would invite a caller
// back before a token exists, so sub-second refills round up and nothing
// rounds down.
const (
	minFloodRetryAfter = time.Second
	shedRetryAfter     = 5 * time.Second
	depthWarnInterval  = time.Minute
)

const maxDedupeEntries = 20000

type floodControl struct {
	policy    FloodPolicy
	runs      *ratelimit.Limiter
	dedupe    *dedupeWindow
	depth     *queueDepthCache
	depthWarn *warnEvery
}

func newFloodControl(p FloodPolicy) *floodControl {
	f := &floodControl{policy: p, depthWarn: &warnEvery{every: depthWarnInterval}}
	if p.RunsPerPrincipalHour > 0 {
		f.runs = ratelimit.New(p.RunsPerPrincipalHour, time.Hour)
	}
	if p.DedupeWindow > 0 {
		f.dedupe = &dedupeWindow{window: p.DedupeWindow, seen: make(map[string]dedupeEntry)}
	}
	if p.ShedQueueDepth > 0 {
		f.depth = &queueDepthCache{}
	}
	return f
}

// safety: a refusal is written here, so a caller that gets false must return without writing its own answer.
func (s *Server) admitTriggerSubmission(w http.ResponseWriter, r *http.Request, key, source string) bool {
	f := s.flood
	if f == nil {
		return true
	}
	now := time.Now()
	if f.depth != nil {
		depth, err := f.depth.read(r.Context(), s.store, now)
		if err != nil {
			// safety: a depth this controller cannot read is not grounds to
			// refuse work, so the submission proceeds and the cap still binds.
			if f.depthWarn.due() {
				s.logger.Warn("trigger flood control: queue depth unavailable",
					"principal", key, "source", source, "err", err)
			}
		} else if depth >= f.policy.ShedQueueDepth {
			s.logger.Warn("trigger shed",
				"principal", key, "source", source, "reason", "queue depth above the shed threshold",
				"queue_depth", depth, "threshold", f.policy.ShedQueueDepth)
			writeRetryAfterStatus(w, http.StatusServiceUnavailable, shedRetryAfter,
				"controller queue is above its shed threshold")
			return false
		}
	}
	if f.runs != nil {
		allowed, wait := f.runs.AllowWithRetry(key, now)
		if !allowed {
			wait = max(wait, minFloodRetryAfter)
			s.logger.Warn("trigger shed",
				"principal", key, "source", source, "reason", "hourly run cap reached",
				"cap_per_hour", f.policy.RunsPerPrincipalHour, "retry_after", wait)
			writeRetryAfter(w, wait, "hourly run cap reached for this principal")
			return false
		}
	}
	return true
}

// safety: an unauthenticated delivery has no principal, so the caller supplies
// the fallback the budget is keyed on rather than every such delivery sharing one.
func (s *Server) floodKey(r *http.Request, fallback string) string {
	if p, ok := PrincipalFromContext(r.Context()); ok && p != nil {
		if p.TokenPrefix != "" {
			return "token:" + p.TokenPrefix
		}
		if label := p.label(); label != "" {
			return "principal:" + label
		}
	}
	if fallback != "" {
		return fallback
	}
	return "client:" + s.loginLimit.client(r)
}

type dedupeEntry struct {
	runID string
	at    time.Time
}

// safety: the answer expires rather than persisting, because two identical
// submissions an hour apart are two runs and only a redelivery is one.
type dedupeWindow struct {
	mu     sync.Mutex
	window time.Duration
	seen   map[string]dedupeEntry
}

// safety: looking up and recording under one lock is what stops two simultaneous
// identical submissions from both starting a run.
func (d *dedupeWindow) claim(digest, runID string, now time.Time) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e, ok := d.seen[digest]; ok && now.Sub(e.at) < d.window {
		return e.runID, true
	}
	d.evictLocked(now)
	d.seen[digest] = dedupeEntry{runID: runID, at: now}
	return "", false
}

// safety: a submission the store refused must not shadow the caller's next attempt.
func (d *dedupeWindow) forget(digest string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.seen, digest)
}

// safety: the window bounds this map in time but not under a flood, so a full
// map drops its expired entries first and its coldest half only if that left it full.
func (d *dedupeWindow) evictLocked(now time.Time) {
	if len(d.seen) < maxDedupeEntries {
		return
	}
	for k, e := range d.seen {
		if now.Sub(e.at) >= d.window {
			delete(d.seen, k)
		}
	}
	if len(d.seen) < maxDedupeEntries {
		return
	}
	ages := make([]time.Time, 0, len(d.seen))
	for _, e := range d.seen {
		ages = append(ages, e.at)
	}
	slices.SortFunc(ages, func(a, b time.Time) int { return a.Compare(b) })
	cutoff := ages[len(ages)/2]
	for k, e := range d.seen {
		if !e.at.After(cutoff) {
			delete(d.seen, k)
		}
	}
}

// perf: a flood asks for the depth far faster than it changes, and each answer
// is a table scan, so one read serves every submission in the same second.
const queueDepthTTL = time.Second

// safety: the count must not ride one requester's context, or a client that
// hangs up decides what every other submission sees.
const queueDepthTimeout = 2 * time.Second

type queueDepthCache struct {
	mu    sync.Mutex
	depth int
	at    time.Time
}

type pendingTriggerCounter interface {
	CountPendingTriggers(ctx context.Context) (int, error)
}

func (q *queueDepthCache) read(parent context.Context, counter pendingTriggerCounter, now time.Time) (int, error) {
	q.mu.Lock()
	fresh := !q.at.IsZero() && now.Sub(q.at) < queueDepthTTL
	depth := q.depth
	q.mu.Unlock()
	if fresh {
		return depth, nil
	}
	// safety: the count outlives one requester, so the caller's cancel is
	// dropped while its values and tracing are kept.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), queueDepthTimeout)
	defer cancel()
	counted, err := counter.CountPendingTriggers(ctx)
	if err != nil {
		return 0, err
	}
	q.mu.Lock()
	q.depth, q.at = counted, now
	q.mu.Unlock()
	return counted, nil
}

// safety: a depth that cannot be read fails on every submission of a flood,
// so the line that says so is worth one a window rather than one a request.
type warnEvery struct {
	mu    sync.Mutex
	every time.Duration
	last  time.Time
}

func (w *warnEvery) due() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	if !w.last.IsZero() && now.Sub(w.last) < w.every {
		return false
	}
	w.last = now
	return true
}

// safety: the digest covers everything that decides what the run does, so two
// submissions differing anywhere a run can observe stay two runs.
func submissionDigest(principal string, in triggerIntake) string {
	sum := sha256.New()
	write := func(parts ...string) {
		for _, p := range parts {
			sum.Write([]byte(p))
			sum.Write([]byte{0})
		}
	}
	// safety: a 409 names another caller's run id, so the digest is scoped to
	// the principal and one tenant can never be answered with another's run.
	write(principal)
	write(in.Pipeline, in.Source, in.User, in.ParentRunID, in.ParentNodeID, in.RetryOf)
	write(in.Git.Branch, in.Git.SHA, in.Git.Repo, in.Git.RepoURL, in.Git.GithubOwner, in.Git.GithubRepo)
	writeSortedMap(write, in.Args)
	writeSortedMap(write, in.Env)
	return hex.EncodeToString(sum.Sum(nil))
}

func writeSortedMap(write func(...string), m map[string]string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		write(k, m[k])
	}
}
