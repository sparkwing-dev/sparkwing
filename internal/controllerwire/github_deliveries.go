package controllerwire

import "time"

// FailedGitHubDelivery exposes a failure without exposing the signed payload.
type FailedGitHubDelivery struct {
	ID         string    `json:"id"`
	Event      string    `json:"event"`
	ReceivedAt time.Time `json:"received_at"`
	LastError  string    `json:"last_error"`
}
