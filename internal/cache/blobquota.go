package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
)

// counter asks the controller to count what each team stores in the bucket
// and downloads through the cache; nil without --controller, which leaves
// every team uncounted. counterAuth is the cache's own bearer.
var (
	counter     *storagequota.Client
	counterAuth string
)

func operatorTeam(team string) bool { return storagequota.Exempt(team) }

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

// blobReservation is one upload's hold on its team's cache share. The zero
// value holds nothing and finishes as a no-op.
type blobReservation struct {
	res     storagequota.Reservation
	counted bool
	wrote   bool
	added   int64
}

// stored records what the write added to the bucket, the size difference
// for an overwrite.
func (b *blobReservation) stored(added int64) {
	b.wrote, b.added = true, added
}

// finish commits what the write added, or releases the reservation when
// nothing was written. It outlives the request's context, because an
// upload whose client hung up after the object landed still holds bytes.
func (b *blobReservation) finish(ctx context.Context) {
	if !b.counted {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var err error
	if b.wrote {
		err = counter.Commit(ctx, counterAuth, b.res, b.added)
	} else {
		err = counter.Release(ctx, counterAuth, b.res)
	}
	if err != nil {
		// #nosec G706 -- the team is a checked slug
		log.Printf("warning: count team %s's cache write: %v", b.res.Team, err)
	}
}

// reserveBlobWrite holds room for a team's upload before one byte of it is
// read. A known length is admitted whole or refused with 413; an unknown one
// gets the room left, up to limit, and its body fails past it, which aborts
// the upload before anything is readable. The caller calls finish once the
// write is done, whatever it did.
func reserveBlobWrite(w http.ResponseWriter, r *http.Request, team string, size, limit int64) (*blobReservation, io.Reader, bool) {
	if counter == nil || operatorTeam(team) {
		return &blobReservation{}, r.Body, true
	}
	upTo := size < 0
	want := size
	if upTo {
		want = limit
	}
	res, err := counter.Reserve(r.Context(), counterAuth, team, storagequota.KindCache, want, upTo)
	if err != nil {
		writeQuotaRefusal(w, err)
		return nil, nil, false
	}
	b := &blobReservation{res: res, counted: true}
	if !upTo || res.Unlimited {
		return b, r.Body, true
	}
	return b, &shareReader{r: r.Body, left: res.Granted}, true
}

func writeQuotaRefusal(w http.ResponseWriter, err error) {
	var quota *storagequota.QuotaError
	switch {
	case errors.As(err, &quota) && quota.Paused:
		http.Error(w, err.Error(), http.StatusPaymentRequired)
	case errors.As(err, &quota):
		// #nosec G706 -- the refusal names a checked team slug and byte counts
		log.Printf("free storage refused a write: %v", err)
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
	case errors.Is(err, storagequota.ErrUnavailable):
		log.Printf("warning: team storage cannot be counted: %v", err)
		w.Header().Set("Retry-After", "30")
		http.Error(w, "team storage cannot be counted right now; retry shortly", http.StatusServiceUnavailable)
	default:
		log.Printf("warning: count team storage: %v", err)
		http.Error(w, "count team storage", http.StatusBadGateway)
	}
}

func quotaCut(w http.ResponseWriter, err error, what string) bool {
	if !errors.Is(err, errBeyondShare) {
		return false
	}
	http.Error(w, fmt.Sprintf("%s: %v", what, errBeyondShare), http.StatusRequestEntityTooLarge)
	return true
}
