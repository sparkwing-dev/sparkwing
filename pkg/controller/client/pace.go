package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TokenDeadError reports a 401 whose body says the bearer matched a
// credential that will never authenticate again. A polling loop parks on it
// instead of retrying at its own cadence.
type TokenDeadError struct {
	// State is "revoked" or "expired", as the controller sent it.
	State string
	// Message is the controller's own message.
	Message string
}

func (e *TokenDeadError) Error() string {
	if e.Message == "" {
		return "controller 401: token is " + e.State
	}
	return "controller 401: " + e.Message
}

// Explain is the line a parked loop logs once: it names the token by its
// non-secret prefix, says the controller will not take it again, and gives
// remedy, the way this process picks up a live token.
func (e *TokenDeadError) Explain(token, remedy string) string {
	return fmt.Sprintf("token %s is %s, so the controller will never accept it again. %s. "+
		"Until then this process asks the controller once an hour.",
		TokenPrefix(token), e.State, remedy)
}

// TokenPrefix returns the non-secret prefix that names a token in logs and in
// `sparkwing cluster tokens list`. A token too short to carry one is named
// "(malformed)" rather than printed, since all of it would be the secret.
func TokenPrefix(token string) string {
	if len(token) <= store.PrefixLen {
		return "(malformed)"
	}
	return token[:store.PrefixLen]
}

func tokenDeadFromBody(body []byte) (state, message string, ok bool) {
	var wire struct {
		TokenState string `json:"token_state"`
		Message    string `json:"message"`
	}
	if json.Unmarshal(body, &wire) != nil {
		return "", "", false
	}
	switch wire.TokenState {
	case "revoked", "expired":
		return wire.TokenState, wire.Message, true
	default:
		return "", "", false
	}
}

const (
	// FailureBackoffBase is a polling loop's first wait after a failed request.
	FailureBackoffBase = time.Second
	// FailureBackoffCap bounds the wait consecutive failures double toward.
	FailureBackoffCap = 5 * time.Minute
	// DeadTokenRetry is how long a loop parked on a dead token waits before
	// asking again.
	DeadTokenRetry = time.Hour
)

// Pace is what a polling loop does after a failed request.
type Pace struct {
	// Wait is how long to wait before the next request.
	Wait time.Duration
	// Shed reports that the controller asked for the wait with a 429 or a
	// 503, which is load rather than a fault.
	Shed bool
	// Parked reports that the loop is parked on a dead token and Wait is the
	// hour it waits before asking again.
	Parked bool
	// Dead is the refusal that parked the loop, set only on the failure that
	// parked it: the moment to log the loud error, once.
	Dead *TokenDeadError
}

// Pacer paces a loop that polls the controller through failures, so a dead
// fleet costs the controller a request an hour per agent rather than one per
// poll. Failures back off exponentially from [FailureBackoffBase] to
// [FailureBackoffCap] with jitter; a dead token parks the loop for
// [DeadTokenRetry]. A Pacer belongs to one goroutine.
type Pacer struct {
	floor    time.Duration
	failures int
	parked   bool
}

// NewPacer returns a Pacer that never waits less than floor, the loop's own
// poll cadence. A floor above [FailureBackoffCap] is clamped to it, so a
// failure never waits longer than the cap unless the controller asks.
func NewPacer(floor time.Duration) *Pacer {
	return &Pacer{floor: min(floor, FailureBackoffCap)}
}

// Success records an answered request and resets the backoff. It reports
// whether the loop was parked on a dead token, so the loop can say it resumed.
func (p *Pacer) Success() (resumed bool) {
	resumed = p.parked
	p.failures, p.parked = 0, false
	return resumed
}

// Failure reports how long to wait after err.
func (p *Pacer) Failure(err error) Pace {
	dead := AsTokenDead(err)
	ok := dead != nil
	// safety: only an answered request unparks, so a network fault during the
	// hourly check does not turn a dead fleet back into a fast one.
	if ok || p.parked {
		pace := Pace{Wait: DeadTokenRetry + retryJitter(DeadTokenRetry), Parked: true}
		if !p.parked {
			pace.Dead = dead
		}
		p.parked = true
		return pace
	}
	backoff := min(FailureBackoffBase<<min(p.failures, 16), FailureBackoffCap)
	p.failures++
	// #nosec G404 -- retry spread, not a security decision
	wait := max(backoff-time.Duration(rand.Int64N(int64(backoff/4)+1)), p.floor)
	if advised, ok := UnavailableBackoff(err, p.floor); ok {
		return Pace{Wait: max(wait, advised), Shed: true}
	}
	return Pace{Wait: wait}
}

// IsTokenDead reports whether err carries a [TokenDeadError].
func IsTokenDead(err error) bool {
	return AsTokenDead(err) != nil
}

// AsTokenDead returns the [TokenDeadError] err carries, or nil.
func AsTokenDead(err error) *TokenDeadError {
	var dead *TokenDeadError
	if errors.As(err, &dead) {
		return dead
	}
	return nil
}
