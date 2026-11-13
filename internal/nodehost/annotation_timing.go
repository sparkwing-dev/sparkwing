package nodehost

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
)

type annotationObservationKey struct{}

type annotationObservation struct {
	mu    sync.Mutex
	sink  func(otelutil.OperationTiming)
	id    string
	route string
}

func (o *annotationObservation) observe(timing otelutil.OperationTiming) {
	o.mu.Lock()
	sink := o.sink
	o.mu.Unlock()
	if sink != nil {
		timing.RequestID, timing.Route = o.id, o.route
		sink(timing)
	}
}

var processRoutes = func() *http.ServeMux {
	mux := http.NewServeMux()
	for _, route := range processServiceRouteTable {
		mux.Handle(route.method+" "+route.path, http.NotFoundHandler())
	}
	return mux
}()

// processRoute returns the registered route template, never the request path,
// so SDK-chosen path values such as secret names stay out of diagnostics.
func processRoute(r *http.Request) string {
	_, pattern := processRoutes.Handler(r)
	return pattern
}

func observeProcessRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observation := &annotationObservation{id: rand.Text(), route: processRoute(r)}
		ctx := context.WithValue(r.Context(), annotationObservationKey{}, observation)
		ctx = otelutil.WithTimingObserver(ctx, observation.observe)
		if r.Method != http.MethodPost || r.URL.Path != "/node/v1/annotations" {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		ctx, timing := otelutil.StartOperation(ctx, "nodehost.annotation")
		response := &annotationResponse{ResponseWriter: w, status: 0}
		defer func() {
			err := ctx.Err()
			if response.status >= 400 {
				err = errors.New("annotation refused")
			}
			timing.Finish(err)
		}()
		next.ServeHTTP(response, r.WithContext(ctx))
	})
}

type annotationResponse struct {
	http.ResponseWriter
	status int
}

func (w *annotationResponse) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *annotationResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func withProcessTiming(ctx context.Context, service *ProcessServices) context.Context {
	if observation, ok := ctx.Value(annotationObservationKey{}).(*annotationObservation); ok {
		observation.mu.Lock()
		observation.sink = service.observeTiming
		observation.mu.Unlock()
		return ctx
	}
	if service.observeTiming != nil {
		return otelutil.WithTimingObserver(ctx, service.observeTiming)
	}
	return ctx
}

func (w *annotationResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}
