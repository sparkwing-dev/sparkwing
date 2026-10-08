package cache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
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
	if testing.Short() {
		t.Skip("slow: 1.3s of real work; the fast class runs under -short")
	}
	gitcacheFixture(t)
	setWindows(t, time.Hour, time.Hour)
	countFetches(t, nil)

	cases := []struct {
		name string
		call func()
	}{
		{"info-refs", func() { refsRequest(t) }},
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
	if testing.Short() {
		t.Skip("slow: 10.1s of real work; the fast class runs under -short")
	}
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

func shortenGitForkWait(t *testing.T, d time.Duration) {
	t.Helper()
	old := gitForkWait
	gitForkWait = d
	t.Cleanup(func() { gitForkWait = old })
}

func TestGitSmartHTTPWaitsForAForkSlotBeforeRefusing(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.6s of real work; the fast class runs under -short")
	}
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
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
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
		bgFetch.markRequested(name + ".git")
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
