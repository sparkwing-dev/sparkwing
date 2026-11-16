package githubapp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

type requestMemoKey struct{}

// WithRequestMemo makes the client answer repeated identical reads, and
// repeated identical installation-token requests, made under ctx from the
// first answer. One controller request derives the same repository facts
// several times; each answer is a serial GitHub round trip.
func WithRequestMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestMemoKey{}, &requestMemo{answers: make(map[string]*memoAnswer)})
}

type requestMemo struct {
	mu      sync.Mutex
	answers map[string]*memoAnswer
}

type memoAnswer struct {
	done   chan struct{}
	status int
	header http.Header
	body   []byte
	kept   bool
}

type memoTransport struct{ base http.RoundTripper }

func (t memoTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	memo, _ := req.Context().Value(requestMemoKey{}).(*requestMemo)
	if memo == nil {
		return t.base.RoundTrip(req)
	}
	key, ok, err := memoKey(req)
	if err != nil {
		return nil, err
	}
	if !ok {
		return t.base.RoundTrip(req)
	}
	memo.mu.Lock()
	answer, found := memo.answers[key]
	if !found {
		answer = &memoAnswer{done: make(chan struct{})}
		memo.answers[key] = answer
	}
	memo.mu.Unlock()
	if found {
		select {
		case <-answer.done:
		case <-req.Context().Done():
			return nil, context.Cause(req.Context())
		}
		if answer.kept {
			return answer.response(req), nil
		}
		return t.RoundTrip(req)
	}
	defer func() {
		if !answer.kept {
			memo.forget(key, answer)
		}
		close(answer.done)
	}()
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	// safety: callers bound what they read; a body past the cap streams through unkept.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMemoBody+1))
	if err != nil || len(body) > maxMemoBody {
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), failingReader(err, resp.Body)), resp.Body}
		return resp, nil
	}
	if err := resp.Body.Close(); err != nil {
		return nil, err
	}
	answer.status, answer.header, answer.body = resp.StatusCode, resp.Header.Clone(), body
	// safety: only settled facts repeat; a throttle or server fault must be asked again.
	answer.kept = resp.StatusCode < 300 || resp.StatusCode == http.StatusNotFound
	return answer.response(req), nil
}

// safety: a later caller asks GitHub again instead of reusing an unsettled answer.
func (m *requestMemo) forget(key string, answer *memoAnswer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.answers[key] == answer {
		delete(m.answers, key)
	}
}

// safety: a memo keeps only metadata-sized answers; larger ones stream through unkept.
const maxMemoBody = 1 << 20

func failingReader(err error, rest io.Reader) io.Reader {
	if err != nil {
		return errReader{err: err}
	}
	return rest
}

// CloseIdleConnections reaches the wrapped transport, which owns the connections.
func (t memoTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (a *memoAnswer) response(req *http.Request) *http.Response {
	return &http.Response{
		Status: strconv.Itoa(a.status) + " " + http.StatusText(a.status), StatusCode: a.status, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: a.header.Clone(), Body: io.NopCloser(bytes.NewReader(a.body)), ContentLength: int64(len(a.body)), Request: req,
	}
}

// safety: a token request's App JWT changes on every call, so its key is the installation and scope;
// any other write is never answered from memory.
func memoKey(req *http.Request) (string, bool, error) {
	selector := req.Method + " " + req.URL.String() + " " + req.Header.Get("Accept") + " " + req.Header.Get("X-GitHub-Api-Version")
	switch {
	case req.Method == http.MethodGet:
		return selector + " " + req.Header.Get("Authorization"), true, nil
	case req.Method == http.MethodPost && strings.HasPrefix(req.URL.Path, "/app/installations/") && strings.HasSuffix(req.URL.Path, "/access_tokens"):
		if req.GetBody == nil {
			return "", false, nil
		}
		body, err := req.GetBody()
		if err != nil {
			return "", false, err
		}
		scope, err := io.ReadAll(body)
		if err != nil {
			return "", false, err
		}
		return selector + " " + string(scope), true, nil
	}
	return "", false, nil
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }
