package store

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// TeamSettings reads the persisted enrollment policy and claim ranking.
type TeamSettings struct {
	MembersCanAddMachines bool     `json:"members_can_add_machines"`
	ClaimRanking          []string `json:"claim_ranking"`
}

// TeamSettingsUpdate changes the named enrollment and ranking settings.
type TeamSettingsUpdate struct {
	MembersCanAddMachines *bool     `json:"members_can_add_machines,omitempty"`
	ClaimRanking          *[]string `json:"claim_ranking,omitempty"`
}

// FailedGitHubDelivery describes a failed inbox entry without its signed payload.
type FailedGitHubDelivery struct {
	ID         string    `json:"id"`
	Event      string    `json:"event"`
	ReceivedAt time.Time `json:"received_at"`
	LastError  string    `json:"last_error"`
}

// OperatorRefund records a completed provider refund against its persisted payer.
type OperatorRefund struct {
	PaymentID   string `json:"payment_id"`
	RefundID    string `json:"refund_id"`
	AmountCents int64  `json:"amount_cents"`
	Reason      string `json:"reason"`
}

// ScheduleRepoRequest reconciles one repository’s schedule declarations.
type ScheduleRepoRequest struct {
	Mode      ScheduleWriteMode             `json:"mode"`
	RepoURL   string                        `json:"repo_url"`
	Branch    string                        `json:"branch,omitempty"`
	SHA       string                        `json:"sha,omitempty"`
	Follow    bool                          `json:"follow"`
	Schedules []runspec.ScheduleDeclaration `json:"schedules"`
}

// ScheduleWriteMode selects complete replacement or declaration upserts.
type ScheduleWriteMode string

// ApprovalDecision records an authenticated human decision with its replay key.
type ApprovalDecision struct {
	Resolution     string `json:"resolution"`
	Comment        string `json:"comment,omitempty"`
	IdempotencyKey string `json:"-"`
}

// Schedule write modes preserve unselected declarations during upserts.
const (
	ScheduleReplace ScheduleWriteMode = "replace"
	ScheduleUpsert  ScheduleWriteMode = "upsert"
)

// ApprovalResult records the persisted decision and its replay status.
type ApprovalResult struct {
	Approval Approval `json:"approval"`
	Replayed bool     `json:"replayed"`
}
