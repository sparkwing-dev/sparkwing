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

	// safety: a caller that has already gone away must not wait out the fork-slot window.
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

func shortenGitForkWait(t *testing.T, d time.Duration) {
	t.Helper()
	old := gitForkWait
	gitForkWait = d
	t.Cleanup(func() { gitForkWait = old })
}

func TestGitSmartHTTPWaitsForAForkSlotBeforeRefusing(t *testing.T) {
	_, bareRepo, _ := gitcacheFixture(t)
	isolateRepoNames(t)
	repoNamesMu.Lock()
	repoNames["widgets"] = "https://git.example.com/acme/widgets.git"
	repoNamesMu.Unlock()
	if _, err := os.Stat(bareRepo); err != nil {
		t.Fatal(err)
	}

	const wait = 300 * time.Millisecond
	shortenGitForkWait(t, wait)
	release := holdGitForkSlot(t)
	defer release()

	req := httptest.NewRequest(http.MethodGet, "/git/widgets/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()
	start := time.Now()
	handleGit(w, req)
	elapsed := time.Since(start)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503 after the fork-slot window expires", w.Code)
	}
	if elapsed < wait {
		t.Errorf("refused after %s, want a wait of at least %s", elapsed, wait)
	}
}

func TestGitSmartHTTPServesTheRequestThatWinsALateSlot(t *testing.T) {
	_, bareRepo, _ := gitcacheFixture(t)
	isolateRepoNames(t)
	repoNamesMu.Lock()
	repoNames["widgets"] = "https://git.example.com/acme/widgets.git"
	repoNamesMu.Unlock()
	if _, err := os.Stat(bareRepo); err != nil {
		t.Fatal(err)
	}

	shortenGitForkWait(t, 10*time.Second)
	release := holdGitForkSlot(t)
	go func() {
		time.Sleep(150 * time.Millisecond)
		release()
	}()

	req := httptest.NewRequest(http.MethodGet, "/git/widgets/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()
	handleGit(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 once a slot frees: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "service=git-upload-pack") {
		t.Errorf("body is not a refs advertisement: %q", w.Body.String())
	}
}

func TestForkExhaustionIsNotReportedAsAMissingRefOrCommit(t *testing.T) {
	repoURL, bareRepo, _ := gitcacheFixture(t)
	setWindows(t, time.Hour, time.Hour)
	countFetches(t, nil)
	head := strings.TrimSpace(string(mustGitOut(t, bareRepo, "rev-parse", "main")))

	cases := []struct {
		name string
		call func() *httptest.ResponseRecorder
	}{
		{"archive", func() *httptest.ResponseRecorder { return archiveRequest(t, repoURL) }},
		{"file", func() *httptest.ResponseRecorder { return fileRequest(t, repoURL) }},
		{"tree-hash", func() *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodGet, "/tree-hash?repo="+repoURL+"&branch=main", nil)
			w := httptest.NewRecorder()
			handleTreeHash(w, req)
			return w
		}},
		{"branch-contains", func() *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodGet,
				"/branch-contains?repo="+repoURL+"&branch=main&commit="+head, nil)
			w := httptest.NewRecorder()
			handleBranchContains(w, req)
			return w
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shortenGitForkWait(t, 100*time.Millisecond)
			release := holdGitForkSlot(t)
			defer release()

			w := tc.call()
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("status %d body %q, want 503: the repository is fine, the server is saturated",
					w.Code, strings.TrimSpace(w.Body.String()))
			}
			if got := w.Header().Get("Retry-After"); got == "" {
				t.Errorf("503 without Retry-After")
			}
		})
	}
}

func TestBackgroundFetchSkipsARepoALockedHandlerHolds(t *testing.T) {
	oldRepoDir := repoDir
	repoDir = t.TempDir()
	t.Cleanup(func() { repoDir = oldRepoDir })
	resetFetchState(t)

	// safety: the loop walks in name order, so the locked repo must come first for the
	// skip to be what lets the second one through.
	const locked, other = "aaa", "zzz"
	for _, name := range []string{locked, other} {
		if err := os.MkdirAll(filepath.Join(repoDir, name+".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	fetched := make(chan string, 8)
	old := mirrorFetch
	mirrorFetch = func(_ time.Duration, bareRepo string) (string, error) {
		select {
		case fetched <- bareRepo:
		default:
		}
		return "", nil
	}
	t.Cleanup(func() { mirrorFetch = old })

	lock := repoLock(locked)
	lock.Lock()
	defer lock.Unlock()

	startBackgroundFetch(t, 5*time.Millisecond)

	deadline := time.After(5 * time.Second)
	for {
		select {
		case bare := <-fetched:
			if bare == filepath.Join(repoDir, other+".git") {
				return
			}
			t.Fatalf("background fetch touched %s while a handler held its lock", bare)
		case <-deadline:
			t.Fatal("a locked repo blocked the background fetch of every other repo")
		}
	}
}
