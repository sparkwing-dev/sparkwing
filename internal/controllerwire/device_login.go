package controllerwire

import "time"

// Device login kinds: a member CLI token, or a runner enrolled under the approver's authority.
const (
	DeviceLoginMember = "member"
	DeviceLoginRunner = "runner"
)

// Device poll statuses that are not refusals; RFC 8628 names the same states.
const (
	DevicePending  = "pending"
	DeviceSlowDown = "slow_down"
	DeviceApproved = "approved"
)

// Device poll refusal reasons, carried in the error envelope's details.reason.
const (
	DeviceDenied       = "access_denied"
	DeviceExpired      = "expired_token"
	DeviceInvalidGrant = "invalid_grant"
)

// StartDeviceLoginRequest describes what a device asks for. Machine names the device on the approval page.
// Grants narrow a member token; empty asks for the approver's run and log grants. Runner carries an
// enrollment and makes Machine its runner name.
type StartDeviceLoginRequest struct {
	Kind    string               `json:"kind"`
	Machine string               `json:"machine"`
	Grants  []string             `json:"grants,omitempty"`
	Runner  *EnrollRunnerRequest `json:"runner,omitempty"`
}

// DeviceLoginStarted returns the device's secret code once, and the code the user types in the browser.
type DeviceLoginStarted struct {
	DeviceCode       string `json:"device_code"`
	UserCode         string `json:"user_code"`
	VerificationPath string `json:"verification_path"`
	ExpiresIn        int64  `json:"expires_in"`
	Interval         int64  `json:"interval"`
}

// DeviceCodeRequest carries the device code for a poll or a cancellation.
type DeviceCodeRequest struct {
	DeviceCode string `json:"device_code"`
}

// DeviceLoginPoll is a poll's answer. Credential is set once, when Status is approved.
type DeviceLoginPoll struct {
	Status     string        `json:"status"`
	Interval   int64         `json:"interval,omitempty"`
	Credential *CreatedToken `json:"credential,omitempty"`
}

// UserCodeRequest names a device login by the code the user typed.
type UserCodeRequest struct {
	UserCode string `json:"user_code"`
}

// DeviceRunner is the enrollment shown on the approval page.
type DeviceRunner struct {
	Name          string   `json:"name"`
	Personal      bool     `json:"personal"`
	Repos         []string `json:"repos"`
	ReservedPools []string `json:"reserved_pools"`
	Trust         string   `json:"trust"`
	Platform      string   `json:"platform"`
	Launcher      bool     `json:"launcher"`
}

// DeviceLoginLookup is what the approval page shows: the team the credential is minted in and its exact grants.
type DeviceLoginLookup struct {
	UserCode  string        `json:"user_code"`
	Kind      string        `json:"kind"`
	Machine   string        `json:"machine"`
	Team      string        `json:"team"`
	Grants    []string      `json:"grants"`
	Runner    *DeviceRunner `json:"runner,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	ExpiresAt time.Time     `json:"expires_at"`
}
