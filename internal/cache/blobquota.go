package cache

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
)

// blobQuota holds each team to its cache share of the free allowance,
// against the bucket's running count, whenever blobStore is set. The
// volume-backed stores carry no allowance: a deployment with more than one
// team keeps its blobs in a bucket.
var blobQuota *storagequota.Quota

func newBlobQuota(store *teamblob.Store, cfg Config) *storagequota.Quota {
	var lookup storagequota.Lookup
	switch {
	case cfg.ControllerURL != "":
		lookup = storagequota.HTTPLookup(cfg.ControllerURL, cfg.APIToken, nil)
	case cfg.GrantKey != "":
		log.Printf("warning: sparkwing-cache has grants but no --controller, so every team a grant names " +
			"is held to the default free share whatever it pays")
	}
	return storagequota.New(storagequota.Options{
		Share:  storagequota.CacheShare,
		Used:   func(team string) int64 { return store.Usage().Team(team).Bytes },
		Lookup: lookup,
		Exempt: func(team string) bool { return team == "" || team == authwire.OperatorTeam },
	})
}

var errBeyondShare = errors.New("the upload passed the room the team's free share left it; add credits to store more")

// shareReader fails a body once it passes the bytes a reservation granted.
type shareReader struct {
	r    io.Reader
	left int64
}

func (s *shareReader) Read(p []byte) (int, error) {
	if s.left <= 0 {
		var one [1]byte
		n, err := s.r.Read(one[:])
		if n > 0 {
			return 0, errBeyondShare
		}
		return 0, err
	}
	if int64(len(p)) > s.left {
		p = p[:s.left]
	}
	n, err := s.r.Read(p)
	s.left -= int64(n)
	return n, err
}

// reserveBlobWrite holds room for a team's upload before one byte of it is
// read. A known length is admitted whole or refused with 413; an unknown one
// gets the room left, up to limit, and its body fails past it, which aborts
// the upload before anything is readable. The caller calls release once the
// write is done, whatever it did.
func reserveBlobWrite(w http.ResponseWriter, r *http.Request, team string, size, limit int64) (func(), io.Reader, bool) {
	if size >= 0 {
		release, err := blobQuota.Reserve(r.Context(), team, size)
		if err != nil {
			writeQuotaRefusal(w, err)
			return nil, nil, false
		}
		return release, r.Body, true
	}
	granted, release, err := blobQuota.ReserveUpTo(r.Context(), team, limit)
	if err != nil {
		writeQuotaRefusal(w, err)
		return nil, nil, false
	}
	return release, &shareReader{r: r.Body, left: granted}, true
}

func writeQuotaRefusal(w http.ResponseWriter, err error) {
	status := http.StatusRequestEntityTooLarge
	if errors.Is(err, storagequota.ErrPaused) {
		status = http.StatusPaymentRequired
	}
	// #nosec G706 -- the refusal names a checked team slug and byte counts
	log.Printf("free storage refused a write: %v", err)
	http.Error(w, err.Error(), status)
}

func quotaCut(w http.ResponseWriter, err error, what string) bool {
	if !errors.Is(err, errBeyondShare) {
		return false
	}
	http.Error(w, fmt.Sprintf("%s: %v", what, errBeyondShare), http.StatusRequestEntityTooLarge)
	return true
}
