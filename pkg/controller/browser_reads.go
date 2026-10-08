package controller

import (
	"context"
	"io"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
)

// safety: the run-scoped reads sit behind the team boundary, which answers another team's run as missing; the
// run list behind them is the request's own team's, so a grep never reaches another team's logs.
func (s *Server) dashboardRead(read func(backend.Backend) http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = withLogsReader(r)
		tenant, ok := s.requestTenant(w, r)
		if !ok {
			return
		}
		b := backend.NewStoreBackend(s.store, s.dashboardPaths(), s.dashboardLogs()).
			Scoped(tenant, liveLogReader{s.liveLogs})
		read(b).ServeHTTP(w, r)
	})
}

// safety: learned capacity profiles are machine-wide rows with no team, so only a host's own dashboard reads them.
func (s *Server) capacityRead(read func(web.ProfileReader) http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reader web.ProfileReader
		if s.dashboard != nil && s.dashboard.Local {
			reader = s.store
		}
		read(reader).ServeHTTP(w, r)
	})
}

func (s *Server) dashboardPaths() paths.Paths {
	if s.dashboard != nil {
		return s.dashboard.Paths
	}
	return paths.Paths{}
}

// safety: a read made through the logs service carries the caller's own credential, because the logs service asks
// this controller whose run it is; a credential of the controller's own would answer for the operator's team.
func (s *Server) dashboardLogs() storage.LogStore {
	if s.dashboard != nil && s.dashboard.Logs != nil {
		return s.dashboard.Logs
	}
	if s.logsURL == "" {
		return nil
	}
	s.forwardedLogsOnce.Do(func() {
		s.forwardedLogs = sparkwinglogs.New(s.logsURL, &http.Client{
			Transport: forwardCredential{base: otelutil.WrapTransport(nil)},
		}, "")
	})
	return s.forwardedLogs
}

type forwardedCredentialKey struct{}

type forwardCredential struct{ base http.RoundTripper }

func (t forwardCredential) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Del("Authorization")
	if credential, _ := req.Context().Value(forwardedCredentialKey{}).(string); credential != "" {
		req.Header.Set("Authorization", credential)
	}
	return t.base.RoundTrip(req)
}

// safety: the logs service budgets concurrent reads per reader, so a browser session's reads are named for its
// principal rather than counted against whatever identity the shared client carries.
func withLogsReader(r *http.Request) *http.Request {
	ctx := r.Context()
	p, ok := PrincipalFromContext(ctx)
	credential := r.Header.Get("Authorization")
	if ok && p.session != "" {
		credential = "Session " + p.session
		ctx = logs.WithReaderIdentity(ctx, viewerIdentityPrefix+p.Name)
	}
	return r.WithContext(context.WithValue(ctx, forwardedCredentialKey{}, credential))
}

const viewerIdentityPrefix = "dashboard:"

type liveLogReader struct{ l *liveLogs }

func (l liveLogReader) StreamNodeLiveLog(ctx context.Context, runID, nodeID string, since int64) (io.ReadCloser, error) {
	if _, ok := l.l.Read(runID, nodeID, since); !ok {
		return nil, nil
	}
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(streamLiveLog(ctx, l.l, pipeSink{pw}, runID, nodeID, since))
	}()
	return pr, nil
}

func (l liveLogReader) ReadNodeLiveLog(_ context.Context, runID, nodeID string, since int64) ([]byte, int64, bool, bool, error) {
	chunk, ok := l.l.Read(runID, nodeID, since)
	if !ok {
		return nil, 0, false, false, nil
	}
	return chunk.Data, chunk.Next, chunk.Done, true, nil
}

type pipeSink struct{ *io.PipeWriter }

func (pipeSink) Flush() error { return nil }
