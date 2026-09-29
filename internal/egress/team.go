package egress

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// ChargeFunc charges n bytes to a team's download day. With record false it
// refuses past the team's cap, and n zero asks only whether any room is left;
// with record true it charges n whatever the cap says.
type ChargeFunc func(ctx context.Context, n int64, record bool) error

// ChargeTeam runs next with a writer that charges the team's download day
// before the response's first byte. A 2xx response that names its length is
// charged whole; one that does not is checked for room when it starts and
// charged what it sent once it ends. A refused response is answered by refuse
// and never started, so no download is cut off part way. It returns the error
// of charging a streamed response once it ended, which the caller logs.
func ChargeTeam(w http.ResponseWriter, r *http.Request, charge ChargeFunc,
	refuse func(http.ResponseWriter, error), next http.Handler,
) error {
	if Bodyless(r) || r.Method != http.MethodGet {
		next.ServeHTTP(w, r)
		return nil
	}
	c := &chargedWriter{ResponseWriter: w, ctx: r.Context(), charge: charge, refuse: refuse}
	next.ServeHTTP(c, r)
	if !c.streaming || c.sent == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancel()
	return charge(ctx, c.sent, true)
}

var errDownloadRefused = errors.New("the team's download was refused before its body")

// safety: a response that names its length is charged whole before its first byte, so
// downloads racing for a team's last bytes cannot both start.
type chargedWriter struct {
	http.ResponseWriter
	ctx       context.Context
	charge    ChargeFunc
	refuse    func(http.ResponseWriter, error)
	started   bool
	refused   bool
	streaming bool
	sent      int64
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
	size, err := strconv.ParseInt(c.Header().Get("Content-Length"), 10, 64)
	if err != nil || size < 0 {
		c.streaming, size = true, 0
	}
	if err := c.charge(c.ctx, size, false); err != nil {
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
	c.sent += int64(n)
	return n, err
}

func (c *chargedWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok && !c.refused {
		f.Flush()
	}
}

// Unwrap hands the underlying writer to [http.NewResponseController].
func (c *chargedWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
