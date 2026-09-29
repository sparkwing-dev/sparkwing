package logs

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
)

// WithEgressMeter counts the bytes this service sends to clients. The
// meter counts log reads and the live log stream against the principal
// that asked for them, refusing no byte: log reads are never budgeted. It
// holds a principal to its concurrency caps. Call it before
// [Server.Handler]; it is not safe to call on a serving Server. A nil
// meter leaves every read unmetered.
func (s *Server) WithEgressMeter(m *egress.Meter) *Server {
	if m != nil {
		m = m.WithLogger(s.logger)
	}
	s.egress = m
	publishedEgress.Store(m)
	registerEgressMetrics.Do(func() {
		if err := prometheus.Register(egress.NewCollector(publishedEgress.Load)); err != nil {
			s.logger.Warn("egress metrics unavailable, so the alarm shows only on health and the log", "err", err)
		}
	})
	return s
}

// safety: the default registry is process-wide and a meter belongs to a
// server, so the collector reads the one the latest server was given.
var (
	publishedEgress       atomic.Pointer[egress.Meter]
	registerEgressMetrics sync.Once
)

// EgressRefusalBody is the 429 a read past a concurrency cap answers
// with. Error carries the whole reason, because that is what a client
// prints when a request fails.
type EgressRefusalBody struct {
	Error     string `json:"error"`
	Code      string `json:"code"`
	Principal string `json:"principal,omitempty"`
}

// EgressStreamLimitCode is the `code` member of the 429 body a stream
// past the per-principal concurrency cap answers with.
const EgressStreamLimitCode = "egress_stream_limit"

// EgressDownloadLimitCode is the `code` member of the 429 body a read
// past the per-principal concurrency cap answers with.
const EgressDownloadLimitCode = "egress_download_limit"

// safety: the meter keys on the team and the name the controller resolved
// the bearer to, because a token's name is free text two teams may share; a
// service running with auth off counts every read in one bucket.
func egressPrincipal(r *http.Request) string {
	p, ok := logsPrincipalFromContext(r.Context())
	if !ok || p == nil || p.Name == "" {
		return egress.AnonymousPrincipal
	}
	return egress.TeamPrincipal(p.Team, p.Name)
}

func (s *Server) metered(class egress.Class, next http.Handler) http.Handler {
	return s.meterOn(class, egress.SlotDownload, next)
}

// safety: one browser tab per node multiplies a live stream without
// limit, so the stream surface counts its own slot; a stream lasts as
// long as its node and a read does not.
func (s *Server) meteredStream(class egress.Class, next http.Handler) http.Handler {
	return s.meterOn(class, egress.SlotLogStream, next)
}

func (s *Server) meterOn(class egress.Class, slot egress.Slot, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.egress == nil {
			next.ServeHTTP(w, r)
			return
		}
		principal := egressPrincipal(r)
		// safety: a response the server discards holds no slot and
		// counts nothing.
		if egress.Bodyless(r) {
			next.ServeHTTP(w, r)
			return
		}
		if slot != egress.SlotNone {
			// safety: the slot keys on the authenticated principal alone,
			// because a caller names its own pod and a named pod would buy
			// another slot.
			release, err := s.egress.Open(principal, slot)
			if err != nil {
				s.writeEgressRefusal(w, r, class, err)
				return
			}
			defer release()
		}
		s.egress.Handle(w, r, principal, class, next)
	})
}

func (s *Server) writeEgressRefusal(w http.ResponseWriter, r *http.Request, class egress.Class, err error) {
	body := EgressRefusalBody{Error: err.Error(), Code: EgressDownloadLimitCode}
	retryAfter := egress.SlotRetryAfter
	var concurrency *egress.ConcurrencyError
	if errors.As(err, &concurrency) {
		body.Code, body.Principal = concurrencyCode(concurrency.Slot), concurrency.Principal
		retryAfter = concurrency.RetryAfter
	}

	s.logger.Warn("egress refused",
		"code", body.Code, "principal", body.Principal,
		"class", string(class), "path", r.URL.Path)
	seconds := int64(retryAfter.Seconds())
	if seconds <= 0 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	s.writeEgressJSON(w, body)
}

// safety: the refusal body is what tells a client why its read stopped,
// so neither a body that will not marshal nor a write that fails leaves
// without a line saying so.
func (s *Server) writeEgressJSON(w http.ResponseWriter, body EgressRefusalBody) {
	buf, err := json.Marshal(body)
	if err != nil {
		s.logger.Error("egress refusal body", "err", err)
		buf = []byte(`{"error":"egress concurrency limit reached","code":"` + body.Code + `"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	if _, err := w.Write(buf); err != nil {
		s.logger.Warn("egress refusal not delivered", "err", err)
	}
}

func concurrencyCode(slot egress.Slot) string {
	if slot == egress.SlotDownload {
		return EgressDownloadLimitCode
	}
	return EgressStreamLimitCode
}

// safety: public health reports the alarm without exposing usage or budget totals.
func (s *Server) egressHealth() (map[string]any, []string) {
	if s.egress == nil {
		return map[string]any{"enabled": false}, nil
	}
	state := s.egress.State()
	summary := map[string]any{"enabled": true, "alarm": state.Alarm}
	if !state.Alarm {
		return summary, nil
	}
	return summary, []string{"egress: daily alarm threshold reached"}
}
