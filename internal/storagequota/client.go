// Package storagequota is how the cache and the logs service ask the
// controller to count a team's storage and downloads. The controller keeps
// every count in its database under a row lock, so the services hold no
// count of their own and any number of replicas of them agree.
//
// A write reserves its size before one byte is read ([Client.Reserve]),
// commits what it stored ([Client.Commit]) or releases the room when it
// fails ([Client.Release]). A download charges its size to the team's UTC
// day before the body is sent ([Client.ChargeDownload]).
//
// A controller that cannot answer is [ErrUnavailable], and the caller
// refuses a free team's write or download with 503: an outage never lifts a
// limit. A team the controller answered funded for within [MaxFundedAge]
// proceeds, and the operator's team never asks.
package storagequota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tier is what a team may store.
type Tier string

const (
	// TierFunded stores without a byte limit: a team with credits, or the
	// operator's own team.
	TierFunded Tier = "funded"
	// TierFree holds a free-tier slot and stores up to its shares.
	TierFree Tier = "free"
	// TierNone has neither credits nor a slot and stores nothing.
	TierNone Tier = "none"
)

// Kind names the store a reservation counts against.
type Kind string

const (
	KindCache Kind = "cache"
	KindLogs  Kind = "logs"
)

// OperatorTeam is the operator's own team, which no limit applies to.
const OperatorTeam = "default"

// Exempt reports a team no limit applies to, which never asks the
// controller: the operator's, or none.
func Exempt(team string) bool { return team == "" || team == OperatorTeam }

// MaxFundedAge is how long a funded answer lets a team proceed while the
// controller cannot answer.
const MaxFundedAge = 5 * time.Minute

// ErrUnavailable reports a controller that did not answer, which a caller
// turns into 503 for any team not funded within [MaxFundedAge].
var ErrUnavailable = errors.New("the controller that counts team storage is unavailable")

// QuotaError refuses a write past a team's share, or any write of a team
// with neither credits nor a free-tier slot.
type QuotaError struct {
	Message string
	Paused  bool
}

func (e *QuotaError) Error() string { return e.Message }

// DownloadCapError refuses a download past a team's daily cap.
type DownloadCapError struct {
	Message    string
	RetryAfter time.Duration
}

func (e *DownloadCapError) Error() string { return e.Message }

// Reservation is room a write holds. One with an empty ID was granted while
// the controller could not answer, and its commit and release send nothing.
type Reservation struct {
	ID        string `json:"reservation"`
	Team      string `json:"-"`
	Kind      Kind   `json:"-"`
	Tier      Tier   `json:"tier"`
	Granted   int64  `json:"granted_bytes"`
	Unlimited bool   `json:"unlimited,omitempty"`
}

// EgressTotals is what one service sent in a UTC day and in its month.
type EgressTotals struct {
	Service    string `json:"service"`
	Day        string `json:"day"`
	DayBytes   int64  `json:"day_bytes"`
	Month      string `json:"month"`
	MonthBytes int64  `json:"month_bytes"`
}

// Client calls one controller's counter routes. Every call takes the
// Authorization header value to send: the cache's own bearer, or the
// credential a logs append arrived with.
type Client struct {
	base string
	http *http.Client
	now  func() time.Time

	mu     sync.Mutex
	funded map[string]time.Time
}

// New returns a client of the controller at controllerURL. A nil client
// gets a five-second timeout.
func New(controllerURL string, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &Client{base: strings.TrimRight(controllerURL, "/"), http: client, now: time.Now, funded: map[string]time.Time{}}
}

// WithClock replaces time.Now, for a test.
func (c *Client) WithClock(now func() time.Time) *Client {
	c.now = now
	return c
}

func (c *Client) observe(team string, tier Tier) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tier == TierFunded {
		c.funded[team] = c.now()
		return
	}
	delete(c.funded, team)
}

func (c *Client) recentlyFunded(team string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.funded[team]
	return ok && c.now().Sub(at) < MaxFundedAge
}

// Reserve holds bytes of team's room in kind. With upTo it holds as much of
// bytes as the team has room for, zero or less asking for all of it.
func (c *Client) Reserve(ctx context.Context, auth, team string, kind Kind, bytes int64, upTo bool) (Reservation, error) {
	var out Reservation
	err := c.post(ctx, auth, "/internal/storage/reserve", map[string]any{
		"team": team, "store": kind, "bytes": bytes, "up_to": upTo,
	}, &out)
	if errors.Is(err, ErrUnavailable) && c.recentlyFunded(team) {
		return Reservation{Team: team, Kind: kind, Tier: TierFunded, Granted: max(bytes, 0), Unlimited: true}, nil
	}
	if err != nil {
		return Reservation{}, err
	}
	out.Team, out.Kind = team, kind
	c.observe(team, out.Tier)
	return out, nil
}

// Commit records that r's write added stored bytes to the store; an
// overwrite passes the difference from what it replaced.
func (c *Client) Commit(ctx context.Context, auth string, r Reservation, stored int64) error {
	if r.ID == "" {
		return nil
	}
	return c.post(ctx, auth, "/internal/storage/commit", map[string]any{
		"team": r.Team, "store": r.Kind, "reservation": r.ID, "bytes": stored,
	}, nil)
}

// Release gives back r's room after a write that stored nothing.
func (c *Client) Release(ctx context.Context, auth string, r Reservation) error {
	if r.ID == "" {
		return nil
	}
	return c.post(ctx, auth, "/internal/storage/release", map[string]any{
		"team": r.Team, "reservation": r.ID,
	}, nil)
}

// ChargeDownload charges bytes to team's UTC day, refusing with a
// [*DownloadCapError] past its cap. Zero bytes checks that room is left.
// With record the bytes are charged whatever the cap says, for a stream
// whose size was known only once it ended.
func (c *Client) ChargeDownload(ctx context.Context, auth, team string, bytes int64, record bool) error {
	var out struct {
		Tier Tier `json:"tier"`
	}
	err := c.post(ctx, auth, "/internal/downloads/charge", map[string]any{
		"team": team, "bytes": bytes, "record": record,
	}, &out)
	if errors.Is(err, ErrUnavailable) && c.recentlyFunded(team) {
		return nil
	}
	if err == nil {
		c.observe(team, out.Tier)
	}
	return err
}

// RecordEgress keeps the larger of each stored total and t's, and returns
// the stored totals.
func (c *Client) RecordEgress(ctx context.Context, auth string, t EgressTotals) (EgressTotals, error) {
	var out EgressTotals
	err := c.post(ctx, auth, "/internal/egress/totals", t, &out)
	return out, err
}

type errorBody struct {
	Error string `json:"error"`
}

func (c *Client) post(ctx context.Context, auth, path string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", auth)
	// #nosec G704 -- the origin is operator configuration and the body names a checked team slug
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 == 2 {
		if out == nil || resp.StatusCode == http.StatusNoContent {
			return nil
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out); err != nil {
			return fmt.Errorf("%w: %s answered unreadably: %v", ErrUnavailable, path, err)
		}
		return nil
	}
	msg := readError(resp)
	switch resp.StatusCode {
	case http.StatusRequestEntityTooLarge:
		return &QuotaError{Message: msg}
	case http.StatusPaymentRequired:
		return &QuotaError{Message: msg, Paused: true}
	case http.StatusTooManyRequests:
		secs, _ := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 64)
		return &DownloadCapError{Message: msg, RetryAfter: time.Duration(max(secs, 1)) * time.Second}
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w: %s answered %d: %s", ErrUnavailable, path, resp.StatusCode, msg)
	}
	return fmt.Errorf("controller %s answered %d: %s", path, resp.StatusCode, msg)
}

func readError(resp *http.Response) string {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	var body errorBody
	if json.Unmarshal(raw, &body) == nil && body.Error != "" {
		return body.Error
	}
	return strings.TrimSpace(string(raw))
}
