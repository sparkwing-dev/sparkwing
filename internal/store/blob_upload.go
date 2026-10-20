package store

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

// UploadReservationRequest binds a server-selected ticket to its uploader and immutable upload facts.
type UploadReservationRequest struct {
	ID        string
	Intent    blob.UploadIntent
	ExpiresAt time.Time
}

// UploadReservation preserves the ticket, uploader, expiry and derived object keys.
type UploadReservation struct {
	ID          string
	Blob        blob.Blob
	Identity    blob.UploadIdentity
	Provenance  blob.Provenance
	RefFacts    blob.RefFacts
	StagingKey  string
	ObjectKey   string
	ExpiresAt   time.Time
	CommittedAt *time.Time
}

// VerifiedUploadCommit carries server-observed object evidence and the configured replica identity.
type VerifiedUploadCommit struct {
	Authority blob.Authority
	TicketID  string
	Verified  blob.Blob
	ReplicaID string
}
