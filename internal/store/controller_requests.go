package store

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// TeamSettings reads the persisted enrollment policy, claim ranking, child-run
// limits and cache trust. HighTrust lets verified runs on plain-machine and
// persistent runners write the shared verified cache.
type TeamSettings struct {
	MembersCanAddMachines bool     `json:"members_can_add_machines"`
	ClaimRanking          []string `json:"claim_ranking"`
	MaxChildrenPerRoot    int64    `json:"max_children_per_root"`
	HighTrust             bool     `json:"high_trust"`
}

// TeamSettingsUpdate changes the named enrollment, ranking, child-run and cache trust settings.
type TeamSettingsUpdate struct {
	MembersCanAddMachines *bool     `json:"members_can_add_machines,omitempty"`
	ClaimRanking          *[]string `json:"claim_ranking,omitempty"`
	MaxChildrenPerRoot    *int64    `json:"max_children_per_root,omitempty"`
	HighTrust             *bool     `json:"high_trust,omitempty"`
}

// FailedGitHubDelivery describes a failed inbox entry without its signed payload.
type FailedGitHubDelivery struct {
	ID         string    `json:"id"`
	Event      string    `json:"event"`
	ReceivedAt time.Time `json:"received_at"`
	LastError  string    `json:"last_error"`
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
	scheduleReplace ScheduleWriteMode = "replace"
	scheduleUpsert  ScheduleWriteMode = "upsert"
)

// ApprovalResult records the persisted decision and its replay status.
type ApprovalResult struct {
	Approval Approval `json:"approval"`
	Replayed bool     `json:"replayed"`
}
