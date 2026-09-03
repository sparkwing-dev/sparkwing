package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// holdGitForkSlot shrinks the fork limit to one and takes it, so any git fork
// under test has to wait. The returned func gives the slot back.
func holdGitForkSlot(t *testing.T) func() {
	t.Helper()
	old := gitForkSem
	gitForkSem = make(chan struct{}, 1)
	t.Cleanup(func() { gitForkSem = old })

	held := gitForkSem
	held <- struct{}{}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		<-held
	}
}

func TestEveryGitCallSiteWaitsForAForkSlot(t *testing.T) {
	repoURL, bareRepo, _ := gitcacheFixture(t)
	setWindows(t, time.Hour, time.Hour)
	countFetches(t, nil)
	head := strings.TrimSpace(string(mustGitOut(t, bareRepo, "rev-parse", "main")))

	cases := []struct {
		name string
		call func()
	}{
		{"archive", func() { archiveRequest(t, repoURL) }},
		{"file", func() { fileRequest(t, repoURL) }},
		{"tree-hash", func() {
			req := httptest.NewRequest(http.MethodGet, "/tree-hash?repo="+repoURL+"&branch=main", nil)
			handleTreeHash(httptest.NewRecorder(), req)
		}},
		{"branch-contains", func() {
			req := httptest.NewRequest(http.MethodGet,
				"/branch-contains?repo="+repoURL+"&branch=main&commit="+head, nil)
			handleBranchContains(httptest.NewRecorder(), req)
		}},
		{"sync-negotiate", func() {
			req := httptest.NewRequest(http.MethodPost, "/sync/negotiate",
				strings.NewReader(`{"repo":"`+repoURL+`","commits":["`+head+`"]}`))
			handleSyncNegotiate(httptest.NewRecorder(), req)
		}},
		{"archive-to-dir", func() {
			_ = archiveToDir(bareRepo, head, t.TempDir())
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			release := holdGitForkSlot(t)
			defer release()

			done := make(chan struct{})
			go func() {
				defer close(done)
				tc.call()
			}()

			select {
			case <-done:
				t.Fatal("git forked while the only fork slot was held")
			case <-time.After(200 * time.Millisecond):
			}

			release()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("call never completed after the fork slot was released")
			}
		})
	}
}

func TestGitSmartHTTPRefusesWhenNoForkSlotIsFree(t *testing.T) {
	_, bareRepo, _ := gitcacheFixture(t)
	isolateRepoNames(t)
	repoNamesMu.Lock()
	repoNames["widgets"] = "https://git.example.com/acme/widgets.git"
	repoNamesMu.Unlock()
	if _, err := os.Stat(bareRepo); err != nil {
		t.Fatal(err)
	}

	release := holdGitForkSlot(t)
	defer release()

	// A request whose caller has already gone away must not wait out the whole
	// fork-slot window before answering.
	gone, cancel := context.WithCancel(context.Background())
	cancel()

	for _, path := range []string{
		"/git/widgets/info/refs?service=git-upload-pack",
		"/git/widgets/git-upload-pack",
	} {
		req := httptest.NewRequest(http.MethodGet, path, strings.NewReader("")).WithContext(gone)
		w := httptest.NewRecorder()
		handleGit(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d, want 503 when no fork slot is free", path, w.Code)
		}
		if got := w.Header().Get("Retry-After"); got == "" {
			t.Errorf("%s: 503 without Retry-After", path)
		}
	}
}

// gitForkCounter puts a git shim first on PATH and returns how many times git
// has been forked since.
func gitForkCounter(t *testing.T) func() int {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}

	dir := t.TempDir()
	tally := filepath.Join(dir, "forks")
	shim := fmt.Sprintf("#!/bin/sh\nprintf 'x' >> %q\nexec %q \"$@\"\n", tally, real)
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return func() int {
		data, err := os.ReadFile(tally)
		if err != nil {
			return 0
		}
		return len(data)
	}
}

func TestSyncNegotiateForksOnceRegardlessOfCommitCount(t *testing.T) {
	repoURL, bareRepo, _ := gitcacheFixture(t)
	setWindows(t, time.Hour, time.Hour)
	countFetches(t, nil)
	head := strings.TrimSpace(string(mustGitOut(t, bareRepo, "rev-parse", "main")))

	commits := make([]string, maxNegotiateCommits)
	for i := range commits {
		commits[i] = fmt.Sprintf("%040x", i+1)
	}
	commits[len(commits)-1] = head
	payload, err := json.Marshal(map[string]any{"repo": repoURL, "commits": commits})
	if err != nil {
		t.Fatal(err)
	}

	forks := gitForkCounter(t)

	req := httptest.NewRequest(http.MethodPost, "/sync/negotiate", strings.NewReader(string(payload)))
	w := httptest.NewRecorder()
	handleSyncNegotiate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Ancestor string `json:"ancestor"`
		Found    bool   `json:"found"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Found || resp.Ancestor != head {
		t.Errorf("ancestor %q found=%v, want %s", resp.Ancestor, resp.Found, head)
	}
	if n := forks(); n != 1 {
		t.Errorf("git forks for %d commits: got %d, want 1", len(commits), n)
	}
}

func TestFirstCachedObjectReportsNothingWhenNoObjectIsPresent(t *testing.T) {
	_, bareRepo, _ := gitcacheFixture(t)

	ids := []string{
		strings.Repeat("a", 40),
		strings.Repeat("b", 40),
	}
	got, err := firstCachedObject(bareRepo, ids)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("ancestor %q, want none", got)
	}
}
