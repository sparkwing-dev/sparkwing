package client

import (
	"encoding/json"
	"errors"
	"fmt"

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
