package controller

import (
	"bytes"
	"crypto/md5" // #nosec G501 -- existing signed log transport uses Content-MD5.
	"encoding/base64"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSignedLogKeyCannotBeReplaced(t *testing.T) {
	property := func(first, second [32]byte) bool {
		second[0] = first[0] ^ 1
		st := new(store.Store)
		st.SetOutputDir(t.TempDir())
		s := &Server{store: st, outputSigner: newOutputSigner(), logger: slog.Default()}
		key := "logs/standard/teams/team/runs/run/nodes/job/attempt-1-gen-1/seg-000001.ndjson.gz"
		put := func(body []byte) int {
			sum := md5.Sum(body)
			md := base64.StdEncoding.EncodeToString(sum[:])
			q := s.outputSigner.sign("log-segment", "team", logPutSubject(key, int64(len(body)), md), time.Now().Add(time.Minute).Unix())
			q.Set("size", strconv.Itoa(len(body)))
			q.Set("md5", md)
			r := httptest.NewRequest(http.MethodPut, "/?"+q.Encode(), bytes.NewReader(body))
			r.SetPathValue("key", key)
			w := httptest.NewRecorder()
			s.handleLogSegmentPut(w, r)
			return w.Code
		}
		if put(first[:]) != http.StatusOK || put(first[:]) != http.StatusOK || put(second[:]) != http.StatusConflict {
			return false
		}
		r, err := s.logObjects().Get(t.Context(), key)
		if err != nil {
			return false
		}
		body, err := io.ReadAll(r)
		closeErr := r.Close()
		return err == nil && closeErr == nil && bytes.Equal(body, first[:])
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32, Rand: rand.New(rand.NewSource(7))}); err != nil {
		t.Fatal(err)
	}
}
