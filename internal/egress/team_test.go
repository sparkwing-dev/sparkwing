package egress_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
)

type teamDay struct {
	used, cap int64
	charges   []int64
}

func (d *teamDay) charge(_ context.Context, n int64, record bool) error {
	if !record && d.used >= d.cap {
		return errors.New("at the cap")
	}
	d.used += n
	if n > 0 {
		d.charges = append(d.charges, n)
	}
	return nil
}

func serveCharged(d *teamDay, handler http.HandlerFunc) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	refuse := func(w http.ResponseWriter, err error) { http.Error(w, err.Error(), http.StatusTooManyRequests) }
	egress.ChargeTeam(rec, httptest.NewRequest(http.MethodGet, "/a", nil), d.charge, refuse, handler)
	return rec
}

// A download is charged what it delivered, not what it declared, so a range
// or a response cut short pays for the bytes sent.
func TestChargeTeamChargesTheBytesDelivered(t *testing.T) {
	d := &teamDay{cap: 1000}
	rec := serveCharged(d, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "500")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(strings.Repeat("x", 120)))
	})
	if rec.Code != http.StatusPartialContent || d.used != 120 {
		t.Fatalf("a range of 120 declared as 500 = %d, charged %d; want 120", rec.Code, d.used)
	}
}

// A response with no length is counted a chunk at a time as it streams, so
// a long stream shows on the team's day before it ends.
func TestChargeTeamCountsALengthlessStreamAsItStreams(t *testing.T) {
	d := &teamDay{cap: 1 << 40}
	chunk := make([]byte, egress.ChargeChunk)
	serveCharged(d, func(w http.ResponseWriter, _ *http.Request) {
		for range 2 {
			_, _ = w.Write(chunk)
		}
		_, _ = w.Write([]byte("tail"))
	})
	want := []int64{egress.ChargeChunk, egress.ChargeChunk, 4}
	if len(d.charges) != len(want) || d.charges[0] != want[0] || d.charges[1] != want[1] || d.charges[2] != want[2] {
		t.Fatalf("charges = %v, want %v", d.charges, want)
	}
}

// Only a team already at its cap is refused; the download that crosses the
// cap is served whole.
func TestChargeTeamRefusesOnlyATeamAtItsCap(t *testing.T) {
	d := &teamDay{used: 90, cap: 100}
	body := strings.Repeat("x", 50)
	serve := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write([]byte(body))
	}
	if rec := serveCharged(d, serve); rec.Code != http.StatusOK || rec.Body.String() != body {
		t.Fatalf("a download with 10 bytes left = %d with %d bytes, want it whole", rec.Code, rec.Body.Len())
	}
	if rec := serveCharged(d, serve); rec.Code != http.StatusTooManyRequests || d.used != 140 {
		t.Fatalf("a download past the cap = %d, used %d; want 429 and nothing more charged", rec.Code, d.used)
	}
}
