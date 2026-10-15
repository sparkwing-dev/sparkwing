package blob_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestFilesystemLogDeletionScope(t *testing.T) {
	property := func(seed uint32, payload [32]byte) bool {
		dir := t.TempDir()
		objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: dir, Endpoint: "https://controller.invalid", SigningKey: bytes.Repeat([]byte{7}, 32)})
		if err != nil {
			t.Log(err)
			return false
		}
		defer func() {
			if err := objects.Close(); err != nil {
				t.Error(err)
			}
		}()
		team, run := fmt.Sprintf("team-%d", seed), fmt.Sprintf("run-%d", seed)
		keys := []string{}
		for _, owner := range []string{team, team + "-other"} {
			for _, id := range []string{run, run + "-other"} {
				for _, class := range []string{"r30", "r90", "r365"} {
					for _, index := range []int64{0, 1} {
						subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: owner, RunID: id, Unit: &blob.LogUnit{Kind: blob.LogPlan, Generation: 1}}
						key, err := blob.LogObjectKey(subject, class, index)
						if err != nil {
							t.Log(err)
							return false
						}
						u, _, err := objects.SignLogPut(t.Context(), key, time.Now().Add(time.Minute))
						if err != nil {
							t.Log(err)
							return false
						}
						w := httptest.NewRecorder()
						objects.ServeHTTP(w, httptest.NewRequest(http.MethodPut, u, bytes.NewReader(payload[:])))
						if w.Code != http.StatusCreated {
							t.Log(w.Code, w.Body)
							return false
						}
						keys = append(keys, key)
					}
				}
			}
		}
		cas := filepath.Join(dir, "teams", team, "cas", string(digest(string(payload[:]))))
		if err := os.MkdirAll(filepath.Dir(cas), 0o700); err != nil {
			t.Log(err)
			return false
		}
		if err := os.WriteFile(cas, payload[:], 0o600); err != nil {
			t.Log(err)
			return false
		}
		for _, invalid := range [][2]string{{"", run}, {"../" + team, run}, {team, "../" + run}, {team, run + "/"}} {
			if n, err := objects.DeleteLogs(t.Context(), invalid[0], invalid[1]); err == nil || n != 0 {
				return false
			}
		}
		for pass, target := range []string{run, "", ""} {
			n, err := objects.DeleteLogs(t.Context(), team, target)
			want := int64(6 * len(payload))
			if pass == 2 {
				want = 0
			}
			if err != nil || n != want {
				t.Log(n, want, err)
				return false
			}
			for _, key := range keys {
				removed := strings.Contains(key, "/teams/"+team+"/") && (pass > 0 || strings.Contains(key, "/runs/"+run+"/"))
				body, err := objects.Get(t.Context(), key)
				if removed {
					if !errors.Is(err, blob.ErrNotFound) {
						return false
					}
					continue
				}
				if err != nil {
					t.Log(err)
					return false
				}
				raw, err := io.ReadAll(body)
				if err = errors.Join(err, body.Close()); err != nil || !bytes.Equal(raw, payload[:]) {
					return false
				}
			}
			if raw, err := os.ReadFile(cas); err != nil || !bytes.Equal(raw, payload[:]) {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64, Rand: rand.New(rand.NewSource(7))}); err != nil {
		t.Fatal(err)
	}
}

func TestFilesystemLogDeletionDoesNotFollowLinks(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: dir, Endpoint: "https://controller.invalid", SigningKey: bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "logs", "r365", "teams"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "logs", "r365", "teams", "acme")); err != nil {
		t.Fatal(err)
	}
	if n, err := objects.DeleteLogs(t.Context(), "acme", ""); n != 0 || err != nil {
		t.Fatalf("delete linked team = %d, %v", n, err)
	}
	if raw, err := os.ReadFile(filepath.Join(outside, "keep")); err != nil || string(raw) != "private" {
		t.Fatalf("external file = %q, %v", raw, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if n, err := objects.DeleteLogs(ctx, "acme", ""); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled delete = %d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "logs", "r365", "teams", "acme")); errors.Is(err, fs.ErrNotExist) {
		t.Fatal("deletion removed a link")
	}
}

func TestFilesystemSignedLogSlotHasOneConcurrentWinner(t *testing.T) {
	property := func(left, right [32]byte) bool {
		right[0] = left[0] ^ 1
		dir := t.TempDir()
		var objects [2]*blob.Filesystem
		for i := range objects {
			var err error
			objects[i], err = blob.NewFilesystem(blob.FilesystemOptions{Directory: dir, Endpoint: "https://controller.invalid", SigningKey: bytes.Repeat([]byte{7}, 32)})
			if err != nil {
				t.Log(err)
				return false
			}
			defer func() {
				if err := objects[i].Close(); err != nil {
					t.Error(err)
				}
			}()
		}
		key := "logs/r90/teams/acme/runs/run/units/plan/g1/seg-000001.ndjson.gz"
		signed, _, err := objects[0].SignLogPut(t.Context(), key, time.Now().Add(time.Minute))
		if err != nil {
			t.Log(err)
			return false
		}
		bodies := [][]byte{left[:], right[:]}
		var statuses [2]int
		start := make(chan struct{})
		var writers sync.WaitGroup
		for i := range objects {
			writers.Go(func() {
				<-start
				response := httptest.NewRecorder()
				objects[i].ServeHTTP(response, httptest.NewRequest(http.MethodPut, signed, bytes.NewReader(bodies[i])))
				statuses[i] = response.Code
			})
		}
		close(start)
		writers.Wait()
		winner := 0
		if statuses[0] != http.StatusCreated {
			winner = 1
		}
		if statuses[winner] != http.StatusCreated || statuses[1-winner] != http.StatusBadRequest {
			t.Log(statuses)
			return false
		}
		body, err := objects[0].Get(t.Context(), key)
		if err != nil {
			t.Log(err)
			return false
		}
		raw, err := io.ReadAll(body)
		return errors.Join(err, body.Close()) == nil && bytes.Equal(raw, bodies[winner])
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64, Rand: rand.New(rand.NewSource(7))}); err != nil {
		t.Fatal(err)
	}
}
