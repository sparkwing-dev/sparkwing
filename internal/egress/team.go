package egress

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"
)

// ChargeFunc charges n bytes to a team's download day. With record false it
// refuses when the team is already at its cap, and n zero asks only that;
// with record true it charges n whatever the cap says.
type ChargeFunc func(ctx context.Context, n int64, record bool) error

// ChargeChunk is how many delivered bytes a download counts at a time, so a
// long response shows on the team's day while it streams.
const ChargeChunk int64 = 8 << 20

// ChargeTeam runs next with a writer that refuses a 2xx response before its
// first byte when the team is already at its cap, then charges the bytes it
// actually delivers, a [ChargeChunk] at a time and the rest when it ends. A
// range or a resumed download is charged what it served, and a response that
// started always finishes. A refused response is answered by refuse.
func ChargeTeam(w http.ResponseWriter, r *http.Request, charge ChargeFunc,
	refuse func(http.ResponseWriter, error), next http.Handler,
) {
	if Bodyless(r) || r.Method != http.MethodGet {
		next.ServeHTTP(w, r)
		return
	}
	c := &chargedWriter{ResponseWriter: w, ctx: r.Context(), charge: charge, refuse: refuse}
	next.ServeHTTP(c, r)
	c.record(r.Context(), 0)
}

var errDownloadRefused = errors.New("the team's download was refused before its body")

type chargedWriter struct {
	http.ResponseWriter
	ctx     context.Context
	charge  ChargeFunc
	refuse  func(http.ResponseWriter, error)
	started bool
	refused bool
	pending int64
}

func (c *chargedWriter) WriteHeader(code int) {
	if c.started {
		c.ResponseWriter.WriteHeader(code)
		return
	}
	c.started = true
	if code/100 != 2 {
		c.ResponseWriter.WriteHeader(code)
		return
	}
	if err := c.charge(c.ctx, 0, false); err != nil {
		c.refused = true
		c.Header().Del("Content-Length")
		c.refuse(c.ResponseWriter, err)
		return
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *chargedWriter) Write(p []byte) (int, error) {
	if !c.started {
		c.WriteHeader(http.StatusOK)
	}
	if c.refused {
		return 0, errDownloadRefused
	}
	n, err := c.ResponseWriter.Write(p)
	c.pending += int64(n)
	c.record(c.ctx, ChargeChunk)
	return n, err
}

// safety: the charge outlives the request, so a client that hangs up still
// pays for what it was sent; a charge that fails is logged, never retried
// into the response.
func (c *chargedWriter) record(ctx context.Context, atLeast int64) {
	if c.refused || c.pending == 0 || c.pending < atLeast {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := c.charge(ctx, c.pending, true); err != nil {
		log.Printf("warning: charge %d delivered bytes to the team's download day: %v", c.pending, err)
	}
	c.pending = 0
}

func (c *chargedWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok && !c.refused {
		f.Flush()
	}
}

// Unwrap hands the underlying writer to [http.NewResponseController].
func (c *chargedWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
