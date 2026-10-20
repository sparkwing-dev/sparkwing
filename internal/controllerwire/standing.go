package controllerwire

// StandingRequestWrite contacts operators for a standing tier, higher limits or larger payments.
type StandingRequestWrite struct {
	Tier    string `json:"tier"`
	Message string `json:"message"`
}

// StandingDecline declines the reviewed open standing request with the reason its requester sees.
type StandingDecline struct {
	Request string `json:"request"`
	Reason  string `json:"reason"`
}

// StandingRequest reports a standing request and its outcome; times are Unix seconds.
type StandingRequest struct {
	ID             string `json:"id"`
	Team           string `json:"team"`
	Tier           string `json:"tier"`
	Message        string `json:"message"`
	RequestedBy    string `json:"requested_by,omitempty"`
	RequestedAt    int64  `json:"requested_at"`
	State          string `json:"state"`
	DecidedAt      int64  `json:"decided_at,omitempty"`
	DecisionReason string `json:"decision_reason,omitempty"`
}

// StandingHistory is the payment record a team's history tier is earned from; times are Unix seconds.
type StandingHistory struct {
	Tier           string `json:"tier"`
	PaidMicro      int64  `json:"paid_micro"`
	FirstPaidAt    int64  `json:"first_paid_at,omitempty"`
	LastIncidentAt int64  `json:"last_incident_at,omitempty"`
}

// TeamStanding is the team's standing, what earned it and its latest request.
type TeamStanding struct {
	Tier          string           `json:"tier"`
	History       StandingHistory  `json:"history"`
	PrepayCeiling string           `json:"prepay_ceiling"`
	CanRequest    bool             `json:"can_request"`
	Request       *StandingRequest `json:"request,omitempty"`
}

// OperatorStandingRequest is one open request in the operator console queue.
type OperatorStandingRequest struct {
	Request       StandingRequest `json:"request"`
	DisplayName   string          `json:"display_name"`
	Standing      string          `json:"standing"`
	PrepayCeiling string          `json:"prepay_ceiling"`
	BalanceMicro  int64           `json:"balance_micro"`
	History       StandingHistory `json:"history"`
}

// OperatorStandingQueue lists open standing requests, oldest first.
type OperatorStandingQueue struct {
	Requests []OperatorStandingRequest `json:"requests"`
}
