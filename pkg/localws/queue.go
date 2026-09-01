package localws

import (
	"context"
	"errors"
	"net/http"
	"time"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func queueHandler(home, version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		qs, err := wingdclient.Query(ctx, wingdclient.Options{Home: home, Version: version})
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
