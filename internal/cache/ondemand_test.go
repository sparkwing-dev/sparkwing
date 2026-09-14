package cache

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func registerMirrorName(t *testing.T, name, repoURL string) {
	t.Helper()
	repoNamesMu.Lock()
	previous, had := repoNames[name]
	repoNames[name] = repoURL
	repoNamesMu.Unlock()
	t.Cleanup(func() {
		repoNamesMu.Lock()
		if had {
			repoNames[name] = previous
		} else {
			delete(repoNames, name)
		}
		repoNamesMu.Unlock()
	})
}

// safety: the fixture clones the mirror before this commit exists, so the push
// is what makes the mirror lag the way a trigger firing seconds after a push does.
func pushCommit(t *testing.T, upstream string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "push")
	mustGit(t, "", "clone", "--quiet", upstream, work)
	mustGit(t, work, "config", "user.email", "t@t")
	mustGit(t, work, "config", "user.name", "t")
	mustGit(t, work, "commit", "--allow-empty", "-m", "pushed after the mirror was cloned")
	mustGit(t, work, "push", "--quiet", "origin", "HEAD:main")
	return strings.TrimSpace(string(mustGitOut(t, work, "rev-parse", "HEAD")))
}

func uploadPackBody(shas ...string) []byte {
	var b bytes.Buffer
	for i, sha := range shas {
		line := "want " + sha + "\n"
		if i == 0 {
			line = "want " + sha + " thin-pack ofs-delta agent=sparkwing-test\n"
		}
		fmt.Fprintf(&b, "%04x%s", len(line)+4, line)
	}
	b.WriteString("0000")
	fmt.Fprintf(&b, "%04x%s", len("done\n")+4, "done\n")
	return b.Bytes()
}

func uploadPackRequest(t *testing.T, name string, shas ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/git/"+name+"/git-upload-pack",
		bytes.NewReader(uploadPackBody(shas...)))
	w := httptest.NewRecorder()
	handleGit(w, req)
	return w
}

func countRealFetches(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old := mirrorFetch
	mirrorFetch = func(timeout time.Duration, bareRepo string) (string, error) {
		n.Add(1)
		return old(timeout, bareRepo)
	}
	t.Cleanup(func() { mirrorFetch = old })
	return &n
}

func mirrorHasCommit(t *testing.T, bareRepo, sha string) bool {
	t.Helper()
	return exec.Command("git", "-C", bareRepo, "cat-file", "-e", sha+"^{commit}").Run() == nil
}

func TestUploadPack_FetchesACommitTheMirrorLacks(t *testing.T) {
	repoURL, bareRepo, upstream := gitcacheFixture(t)
	registerMirrorName(t, "widgets", repoURL)
	fetches := countRealFetches(t)

	sha := pushCommit(t, upstream)
	if mirrorHasCommit(t, bareRepo, sha) {
		t.Fatalf("setup wrong: the mirror already had %s", sha)
	}

	w := uploadPackRequest(t, "widgets", sha)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%q", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "not our ref") {
		t.Errorf("upload-pack refused a commit the on-demand fetch should have brought in: %q", w.Body.String())
	}
	if !mirrorHasCommit(t, bareRepo, sha) {
		t.Errorf("the mirror still lacks %s after upload-pack asked for it", sha)
	}
	if got := fetches.Load(); got != 1 {
		t.Errorf("origin fetches: got %d, want 1", got)
	}
}

func TestUploadPack_BurstOfMissesFetchesOriginOnce(t *testing.T) {
	repoURL, bareRepo, upstream := gitcacheFixture(t)
	registerMirrorName(t, "widgets", repoURL)
	fetches := countRealFetches(t)

	sha := pushCommit(t, upstream)
	if mirrorHasCommit(t, bareRepo, sha) {
		t.Fatalf("setup wrong: the mirror already had %s", sha)
	}

	const triggers = 10
	codes := make([]int, triggers)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = uploadPackRequest(t, "widgets", sha).Code
		}()
	}
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("trigger %d: status %d, want 200", i, code)
		}
	}
	if got := fetches.Load(); got != 1 {
		t.Errorf("origin fetches for %d concurrent triggers on one push: got %d, want 1", triggers, got)
	}
}

func TestUploadPack_CommitOriginDoesNotHaveStillRefusesTheSameWay(t *testing.T) {
	repoURL, _, _ := gitcacheFixture(t)
	registerMirrorName(t, "widgets", repoURL)
	fetches := countRealFetches(t)

	const absent = "d4c0dc18d4c0dc18d4c0dc18d4c0dc18d4c0dc18"
	w := uploadPackRequest(t, "widgets", absent)

	if !strings.Contains(w.Body.String(), "not our ref "+absent) {
		t.Errorf("refusal message changed: got %q, want it to carry \"not our ref %s\"", w.Body.String(), absent)
	}
	if got := fetches.Load(); got != 1 {
		t.Errorf("origin fetches before refusing: got %d, want 1", got)
	}
}

func TestUploadPack_CommitTheMirrorAlreadyHasCostsNoFetch(t *testing.T) {
	repoURL, bareRepo, _ := gitcacheFixture(t)
	registerMirrorName(t, "widgets", repoURL)
	fetches := countRealFetches(t)

	head := strings.TrimSpace(string(mustGitOut(t, bareRepo, "rev-parse", "main")))
	if w := uploadPackRequest(t, "widgets", head); w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%q", w.Code, w.Body.String())
	}

	if got := fetches.Load(); got != 0 {
		t.Errorf("origin fetches for a commit the mirror already had: got %d, want 0", got)
	}
}

func TestParseWants_ReadsBothProtocolVersions(t *testing.T) {
	const sha = "d4c0dc18d4c0dc18d4c0dc18d4c0dc18d4c0dc18"
	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

	v2 := pkt("command=fetch\n") + pkt("object-format=sha1\n") + "0001" +
		pkt("thin-pack\n") + pkt("want "+sha+"\n") + pkt("done\n") + "0000"

	for name, body := range map[string]string{
		"v0":       string(uploadPackBody(sha)),
		"v2":       v2,
		"truncate": pkt("want "+sha+"\n") + "00ff" + pkt("want "+sha+"\n"),
	} {
		t.Run(name, func(t *testing.T) {
			got := parseWants([]byte(body))
			if len(got) != 1 || got[0] != sha {
				t.Errorf("wants: got %v, want [%s]", got, sha)
			}
		})
	}

	if got := parseWants([]byte("garbage")); got != nil {
		t.Errorf("a body that is not pkt-line: got %v, want none", got)
	}
}

func TestScanWants_HandsTheRequestBackToGit(t *testing.T) {
	const sha = "d4c0dc18d4c0dc18d4c0dc18d4c0dc18d4c0dc18"
	body := uploadPackBody(sha)

	wants, consumed := scanWants(bytes.NewReader(body))

	if len(wants) != 1 || wants[0] != sha {
		t.Errorf("wants: got %v, want [%s]", wants, sha)
	}
	if !bytes.Equal(consumed, body) {
		t.Errorf("consumed bytes must be replayable to git: got %q, want %q", consumed, body)
	}
}

func backdateRequest(t *testing.T, hash string, d time.Duration) {
	t.Helper()
	bgFetch.mu.Lock()
	defer bgFetch.mu.Unlock()
	rs := bgFetch.repos[stateKey(hash)]
	if rs == nil || rs.lastRequest.IsZero() {
		t.Fatalf("no request recorded for %s", hash)
	}
	rs.lastRequest = rs.lastRequest.Add(-d)
}

func TestKeepWarm_SkipsAMirrorNoRequestAskedFor(t *testing.T) {
	gitcacheFixture(t)
	fetches := countFetches(t, nil)

	if fetched, failed := keepWarmPass(30 * time.Second); fetched != 0 || failed != 0 {
		t.Errorf("idle mirror: got %d fetched, %d failed; want 0, 0", fetched, failed)
	}
	if got := fetches.Load(); got != 0 {
		t.Errorf("origin fetches for a mirror nobody asked for: got %d, want 0", got)
	}
}

func TestKeepWarm_RefreshesAMirrorARequestAskedForAndStopsWhenItGoesIdle(t *testing.T) {
	repoURL, _, _ := gitcacheFixture(t)
	setWindows(t, time.Minute, time.Hour)
	fetches := countFetches(t, nil)

	if w := fileRequest(t, repoURL); w.Code != http.StatusOK {
		t.Fatalf("file request: status %d, body=%s", w.Code, w.Body.String())
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("setup: the request itself fetched %d times, want 1", got)
	}

	if fetched, _ := keepWarmPass(30 * time.Second); fetched != 1 {
		t.Errorf("mirror inside the keep-warm window: got %d fetched, want 1", fetched)
	}

	backdateRequest(t, repoHash(repoURL), keepWarmWindow+time.Minute)

	if fetched, _ := keepWarmPass(30 * time.Second); fetched != 0 {
		t.Errorf("mirror that went idle: got %d fetched, want 0", fetched)
	}
}

func TestBackgroundFetchLoop_ZeroIntervalPollsNothing(t *testing.T) {
	gitcacheFixture(t)
	fetches := countFetches(t, nil)

	backgroundFetchLoop(context.Background(), 0)

	if got := fetches.Load(); got != 0 {
		t.Errorf("origin fetches with the keep-warm pass off: got %d, want 0", got)
	}
}
