package controller

import (
	"bytes"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGraceEndedAndUnknownCredentialHaveIdenticalHTTPRefusals(t *testing.T) {
	var tick atomic.Int64
	tick.Store(time.Now().Add(time.Minute).UnixNano())
	s, session := credentialSessionServer(t, teststore.WithClock(func() time.Time { return time.Unix(0, tick.Load()) }))
	s.WithEngineController(NewEngineController(s.store, EngineController{VersionPolicy: engineVersionPolicy(&engineVersionFake{})}))
	options, prefix := selectorClientOptions(t, s, session, `{"grants":["runs.read"]}`)
	token, err := options.Bearer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	read := func(raw string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "https://controller.test/api/v1/runs/run", nil)
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		w.Header().Del(RequestIDHeader)
		return w
	}
	if w := read(token); w.Code != 200 {
		t.Fatalf("live credential status=%d", w.Code)
	}
	r := httptest.NewRequest(http.MethodPost, "https://controller.test/api/v1/tokens/"+prefix+"/rotate", strings.NewReader(`{"grace_secs":3}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://controller.test")
	r.Header.Set(csrfHeaderName, "session-csrf")
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "session-csrf"})
	rotated := httptest.NewRecorder()
	s.Handler().ServeHTTP(rotated, r)
	if rotated.Code != 200 {
		t.Fatalf("rotation status=%d", rotated.Code)
	}
	if w := read(token); w.Code != 200 {
		t.Fatalf("within grace status=%d", w.Code)
	}
	tick.Add(int64(4 * time.Second))
	unknown := token[:23] + "A" + token[24:]
	if unknown == token {
		unknown = token[:23] + "B" + token[24:]
	}
	ended, never := read(token), read(unknown)
	if ended.Code != 401 || never.Code != 401 || !bytes.Equal(ended.Body.Bytes(), never.Body.Bytes()) || !reflect.DeepEqual(ended.Header(), never.Header()) {
		t.Fatalf("refusals differ: %d %s %v versus %d %s %v", ended.Code, ended.Body, ended.Header(), never.Code, never.Body, never.Header())
	}
}
