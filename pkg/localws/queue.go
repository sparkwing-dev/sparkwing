package localws

import (
	"context"
	"errors"
	"net/http"
	"time"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// queueHandler serves GET /api/v1/queue: the local admission daemon's
// queue state, in the exact JSON shape the CLI's `sparkwing queue -o
// json` emits, so the dashboard and the CLI show one identical view.
// With no daemon running there is nothing to arbitrate, so it returns a
// well-formed empty queue with 200 rather than an error -- the same calm
// truth the CLI reports.
func queueHandler(home string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		// safety: no Version is sent, so this read-only proxy never
		// drains or replaces a running daemon during the handshake.
		qs, err := wingdclient.Query(ctx, wingdclient.Options{Home: home})
		if err != nil && !errors.Is(err, wingdclient.ErrNoDaemon) {
			http.Error(w, "read admission queue: "+err.Error(), http.StatusBadGateway)
			return
		}
		if errors.Is(err, wingdclient.ErrNoDaemon) {
			qs = wingwire.QueueState{}
		}
		writeJSON(w, qs)
	}
}
