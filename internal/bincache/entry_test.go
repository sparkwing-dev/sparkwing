package bincache

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const seededRecordBytes = 64

func seedEntry(t *testing.T, entry Entry, body string, modTime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(entry.binaryPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry.binaryPath(), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	digest, err := fileDigest(entry.binaryPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry.recordPath(), []byte(digest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Dir(entry.binaryPath()), modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func testEntry(t *testing.T, root, key string) Entry {
	t.Helper()
	entry, err := pipelineEntryAt(root, key)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestPruneSkipsActiveHolderAndReclaimsAnotherEntry(t *testing.T) {
	root := t.TempDir()
	old := testEntry(t, root, "11111111-11111111")
	newer := testEntry(t, root, "22222222-22222222")
	seedEntry(t, old, "old", time.Unix(1, 0))
	seedEntry(t, newer, "newer", time.Unix(2, 0))

	lease, found, err := old.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !found {
		t.Fatal("seeded entry was not found")
	}
	t.Cleanup(func() {
		if err := lease.Release(); err != nil {
			t.Errorf("Release: %v", err)
		}
	})

	result, err := Prune(context.Background(), PruneOptions{
		Root:        root,
		RemoveBytes: 1,
		MaxEntries:  2,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if result.ActiveSkippedEntries != 1 || result.ReclaimedEntries != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if _, err := os.Stat(old.binaryPath()); err != nil {
		t.Fatalf("active entry removed: %v", err)
	}
	if _, err := os.Stat(newer.binaryPath()); !os.IsNotExist(err) {
		t.Fatalf("reclaimable entry remains: %v", err)
	}
}

func TestPruneIsBoundedAndUsesStableOldestOrder(t *testing.T) {
	root := t.TempDir()
	stamp := time.Unix(1, 0)
	first := testEntry(t, root, "11111111-11111111")
	second := testEntry(t, root, "22222222-22222222")
	third := testEntry(t, root, "33333333-33333333")
	seedEntry(t, first, "1", stamp)
	seedEntry(t, second, "22", stamp)
	seedEntry(t, third, "333", stamp)

	result, err := Prune(context.Background(), PruneOptions{
		Root:        root,
		RemoveBytes: math.MaxInt64,
		MaxEntries:  1,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if result.ExaminedEntries != 1 || result.ReclaimedEntries != 1 || result.ObservedBytes != 1+seededRecordBytes {
		t.Fatalf("unexpected bounded accounting: %+v", result)
	}
	if !result.WorkBoundExhausted || result.GoalSatisfied {
		t.Fatalf("bounded miss must be exhausted without satisfying goal: %+v", result)
	}
	if _, err := os.Stat(first.binaryPath()); !os.IsNotExist(err) {
		t.Fatalf("stable first entry remains: %v", err)
	}
	for _, entry := range []Entry{second, third} {
		if _, err := os.Stat(entry.binaryPath()); err != nil {
			t.Fatalf("unexamined entry removed: %v", err)
		}
	}
}

func TestPruneRemainsHealthyAfterRemovingAnEntry(t *testing.T) {
	root := t.TempDir()
	entry := testEntry(t, root, "11111111-11111111")
	seedEntry(t, entry, "remove", time.Unix(1, 0))

	first, err := Prune(context.Background(), PruneOptions{Root: root, RemoveBytes: 1, MaxEntries: 1})
	if err != nil || first.ReclaimedEntries != 1 {
		t.Fatalf("first Prune = (%+v, %v)", first, err)
	}
	second, err := Prune(context.Background(), PruneOptions{Root: root, RemoveBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatalf("second Prune: %v", err)
	}
	if second.ReclaimedEntries != 0 || second.WorkBoundExhausted {
		t.Fatalf("second Prune = %+v", second)
	}
}

func TestCacheCandidateDiscoveryHonorsWorkAndCancellationBounds(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{"11111111-11111111", "22222222-22222222", "33333333-33333333"} {
		seedEntry(t, testEntry(t, root, key), key, time.Unix(1, 0))
	}

	candidates, err := cacheCandidates(context.Background(), root, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) > 2 {
		t.Fatalf("candidate count = %d, want <= 2", len(candidates))
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cacheCandidates(canceled, root, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled discovery error = %v", err)
	}
}

func TestPruneDoesNotRaceWriterOrExposePartialEntry(t *testing.T) {
	root := t.TempDir()
	entry := testEntry(t, root, "11111111-11111111")
	started := make(chan string, 1)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := entry.Materialize(context.Background(), func(path string) error {
			if err := os.WriteFile(path, []byte("complete"), 0o755); err != nil {
				return err
			}
			started <- path
			<-release
			return nil
		})
		done <- err
	}()

	select {
	case staging := <-started:
		if staging == entry.binaryPath() {
			t.Fatal("writer received the final managed path")
		}
	case err := <-done:
		t.Fatalf("Materialize returned before writer entered: %v", err)
	case <-time.After(time.Second):
		t.Fatal("Materialize did not enter writer")
	}

	result, err := Prune(context.Background(), PruneOptions{Root: root, RemoveBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatalf("Prune while writer active: %v", err)
	}
	if result.BusySkippedEntries != 1 || result.ReclaimedEntries != 0 {
		t.Fatalf("active writer was not skipped: %+v", result)
	}
	if _, err := os.Stat(entry.binaryPath()); !os.IsNotExist(err) {
		t.Fatalf("partial final entry became visible: %v", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	body, err := os.ReadFile(entry.binaryPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "complete" {
		t.Fatalf("published body = %q", body)
	}
}

func TestEntryRejectsInvalidKeysAndPruneRejectsInvalidBounds(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{"", "../escape", "AAAAAAAA-BBBBBBBB", "11111111-1111111"} {
		if _, err := pipelineEntryAt(root, key); err == nil {
			t.Errorf("pipelineEntryAt(%q) succeeded", key)
		}
	}

	for _, opts := range []PruneOptions{
		{Root: root, RemoveBytes: 0, MaxEntries: 1},
		{Root: root, RemoveBytes: 1, MaxEntries: 0},
	} {
		if _, err := Prune(context.Background(), opts); err == nil || errors.Is(err, context.Canceled) {
			t.Errorf("Prune(%+v) error = %v", opts, err)
		}
	}
}

func TestPruneAttemptsEveryCandidateAndJoinsRemovalFailures(t *testing.T) {
	root := t.TempDir()
	first := testEntry(t, root, "11111111-11111111")
	second := testEntry(t, root, "22222222-22222222")
	seedEntry(t, first, "first", time.Unix(1, 0))
	seedEntry(t, second, "second", time.Unix(2, 0))

	originalRemove := removeCacheEntry
	t.Cleanup(func() { removeCacheEntry = originalRemove })
	var attempted []string
	removeCacheEntry = func(path string) error {
		attempted = append(attempted, filepath.Base(path))
		return errors.New("remove-" + filepath.Base(path))
	}

	result, err := Prune(context.Background(), PruneOptions{Root: root, RemoveBytes: 100, MaxEntries: 2})
	if err == nil {
		t.Fatal("Prune succeeded despite persistent removal failures")
	}
	if strings.Join(attempted, ",") != "11111111-11111111,22222222-22222222" {
		t.Fatalf("removal attempts = %v", attempted)
	}
	for _, diagnostic := range []string{"remove-11111111-11111111", "remove-22222222-22222222"} {
		if !strings.Contains(err.Error(), diagnostic) {
			t.Errorf("Prune error %q lacks %q", err, diagnostic)
		}
	}
	if result.ObservedBytes != 11+2*seededRecordBytes || result.LogicalRemovedBytes != 0 || result.ReclaimedEntries != 0 {
		t.Fatalf("failed removal accounting: %+v", result)
	}
}

func TestAcquireOrMaterializeClosesPublicationToLeaseGap(t *testing.T) {
	root := t.TempDir()
	entry := testEntry(t, root, "11111111-11111111")
	writes := 0
	lease, published, err := entry.AcquireOrMaterialize(context.Background(), func(path string) error {
		writes++
		return os.WriteFile(path, []byte("leased"), 0o755)
	})
	if err != nil {
		t.Fatalf("AcquireOrMaterialize: %v", err)
	}
	if !published || writes != 1 {
		t.Fatalf("published = %v, writes = %d", published, writes)
	}

	result, err := Prune(context.Background(), PruneOptions{Root: root, RemoveBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatalf("Prune with returned lease: %v", err)
	}
	if result.ActiveSkippedEntries != 1 || result.ReclaimedEntries != 0 {
		t.Fatalf("returned lease did not protect publication: %+v", result)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	lease, published, err = entry.AcquireOrMaterialize(context.Background(), func(string) error {
		writes++
		return errors.New("writer must not run on a hit")
	})
	if err != nil {
		t.Fatalf("AcquireOrMaterialize hit: %v", err)
	}
	if published || writes != 1 {
		t.Fatalf("hit published = %v, writes = %d", published, writes)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release hit: %v", err)
	}
}

func TestAcquireOrMaterializeAutomaticallyPrunesAfterNonCLIWrite(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	writeCacheConfig(t, "cache:\n  max_bytes: 0\n  max_entries: 1\n")

	root := filepath.Join(SparkwingHome(), "cache", "pipelines", pipelineCacheSchema)
	old := testEntry(t, root, "11111111-11111111")
	seedEntry(t, old, "old", time.Unix(1, 0))
	newer := testEntry(t, root, "22222222-22222222")
	lease, published, err := newer.AcquireOrMaterialize(context.Background(), func(path string) error {
		return os.WriteFile(path, []byte("new"), 0o755)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release() }()
	if !published {
		t.Fatal("non-CLI write did not publish")
	}
	if _, err := os.Stat(old.binaryPath()); !os.IsNotExist(err) {
		t.Fatalf("inactive sibling survived automatic pruning: %v", err)
	}
	if _, err := os.Stat(newer.binaryPath()); err != nil {
		t.Fatalf("newly published leased entry was removed: %v", err)
	}
}

func TestAcquireOrMaterializeReportsAutomaticPruneFailure(t *testing.T) {
	oldStatus := statusForLimits
	t.Cleanup(func() { statusForLimits = oldStatus })
	statusForLimits = func(context.Context, string) (CacheStatus, error) {
		return CacheStatus{}, errors.New("read cache inventory")
	}

	entry := testEntry(t, t.TempDir(), "11111111-11111111")
	lease, published, err := entry.AcquireOrMaterialize(context.Background(), func(path string) error {
		return os.WriteFile(path, []byte("complete"), 0o755)
	})
	if err == nil || !strings.Contains(err.Error(), "read cache inventory") {
		t.Fatalf("automatic prune error = %v", err)
	}
	if lease != nil {
		t.Fatal("failed publication returned a live lease")
	}
	if !published {
		t.Fatal("the completed publication was not reported")
	}
	if _, statErr := os.Stat(entry.binaryPath()); statErr != nil {
		t.Fatalf("completed publication disappeared: %v", statErr)
	}
}

func TestConcurrentMaterializationPublishesOnce(t *testing.T) {
	root := t.TempDir()
	entry := testEntry(t, root, "11111111-11111111")
	started := make(chan struct{})
	release := make(chan struct{})
	type outcome struct {
		lease     *Lease
		published bool
		err       error
	}
	results := make(chan outcome, 2)
	go func() {
		lease, published, err := entry.AcquireOrMaterialize(context.Background(), func(path string) error {
			if err := os.WriteFile(path, []byte("complete"), 0o755); err != nil {
				return err
			}
			close(started)
			<-release
			return nil
		})
		results <- outcome{lease: lease, published: published, err: err}
	}()
	<-started
	go func() {
		lease, published, err := entry.AcquireOrMaterialize(context.Background(), func(string) error {
			return errors.New("contending materializer ran")
		})
		results <- outcome{lease: lease, published: published, err: err}
	}()
	close(release)

	published := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.published {
			published++
		}
		if err := result.lease.Release(); err != nil {
			t.Fatal(err)
		}
	}
	if published != 1 {
		t.Fatalf("publishers = %d, want 1", published)
	}
}

func TestStatusMeasuresCurrentCacheLiveness(t *testing.T) {
	cacheRoot := t.TempDir()
	root := filepath.Join(cacheRoot, pipelineCacheSchema)
	active := testEntry(t, root, "11111111-11111111")
	idle := testEntry(t, root, "22222222-22222222")
	seedEntry(t, active, "old", time.Unix(1, 0))
	seedEntry(t, idle, "newer", time.Unix(2, 0))
	legacy := filepath.Join(cacheRoot, "33333333-33333333", "pipelines")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("legacy!"), 0o755); err != nil {
		t.Fatal(err)
	}
	lease, found, err := active.Acquire(context.Background())
	if err != nil || !found {
		t.Fatalf("Acquire active entry = (%v, %v)", found, err)
	}
	defer func() { _ = lease.Release() }()

	status, err := Status(context.Background(), root)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.ObservedBytes != 8+2*seededRecordBytes || status.EntryCount != 2 || status.ActiveEntries != 1 || status.ActiveBytes != 3+seededRecordBytes {
		t.Fatalf("managed status: %+v", status)
	}
}

func TestPruneOnlyCurrentCacheLayout(t *testing.T) {
	cacheRoot := t.TempDir()
	root := filepath.Join(cacheRoot, pipelineCacheSchema)
	entry := testEntry(t, root, "22222222-22222222")
	seedEntry(t, entry, "current", time.Unix(1, 0))
	paths := []string{
		filepath.Join(cacheRoot, "11111111-11111111", "pipelines"),
		filepath.Join(root, "legacy-retired", "33333333-33333333", "pipelines"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("outside current layout"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Prune(t.Context(), PruneOptions{Root: root, ReclaimEntries: 3, MaxEntries: 3})
	if err != nil {
		t.Fatal(err)
	}
	if result.ReclaimedEntries != 1 || result.ExaminedEntries != 1 || result.ObservedBytes != int64(len("current"))+seededRecordBytes {
		t.Fatalf("prune = %+v", result)
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != "outside current layout" {
			t.Fatalf("outside layout %s = %q, %v", path, body, err)
		}
	}
}

func (e Entry) Materialize(ctx context.Context, write func(string) error) (published bool, err error) {
	lease, published, err := e.AcquireOrMaterialize(ctx, write)
	if lease != nil {
		err = errors.Join(err, lease.Release())
	}
	return published, err
}

func cacheCandidates(ctx context.Context, root string, limit int) ([]cacheCandidate, error) {
	candidates, _, err := cacheCandidatesBounded(ctx, root, limit)
	return candidates, err
}

func TestAcquireOrMaterializeRefusesABrokenCeilingOnEveryPath(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	writeCacheConfig(t, "cache:\n  max_bytes: lots\n")
	root := filepath.Join(SparkwingHome(), "cache", "pipelines", pipelineCacheSchema)

	fresh := testEntry(t, root, "33333333-33333333")
	writes := 0
	if _, _, err := fresh.AcquireOrMaterialize(context.Background(), func(path string) error {
		writes++
		return os.WriteFile(path, []byte("new"), 0o755)
	}); err == nil || !strings.Contains(err.Error(), "cache.max_bytes") {
		t.Fatalf("materialize error = %v, want the broken ceiling named", err)
	}
	if writes != 0 {
		t.Fatalf("materializer ran %d times; a broken ceiling must refuse before publishing", writes)
	}

	cached := testEntry(t, root, "44444444-44444444")
	seedEntry(t, cached, "cached", time.Unix(1, 0))
	if _, _, err := cached.AcquireOrMaterialize(context.Background(), func(string) error {
		t.Fatal("a cached entry was rebuilt")
		return nil
	}); err == nil || !strings.Contains(err.Error(), "cache.max_bytes") {
		t.Fatalf("cache-hit error = %v, want the broken ceiling named", err)
	}
}

func TestLeaseRefusesAnExecutableChangedAfterPublication(t *testing.T) {
	entry := testEntry(t, t.TempDir(), "33333333-33333333")
	seedEntry(t, entry, "published", time.Unix(1, 0))
	lease, found, err := entry.Acquire(context.Background())
	if err != nil || !found {
		t.Fatalf("acquire published entry: found=%t err=%v", found, err)
	}
	t.Cleanup(func() {
		if err := lease.Release(); err != nil {
			t.Error(err)
		}
	})
	out := t.TempDir()
	binary, err := lease.Install(filepath.Join(out, "verified"))
	if err != nil {
		t.Fatalf("published executable not verified: %v", err)
	}
	if body, err := os.ReadFile(binary.Path()); err != nil || string(body) != "published" {
		t.Fatalf("verified copy = %q, %v", body, err)
	}
	if err := os.WriteFile(entry.binaryPath(), []byte("planted"), 0o755); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(binary.Path()); err != nil || string(body) != "published" {
		t.Fatalf("rewriting the cache changed the verified copy: %q, %v", body, err)
	}
	tampered := filepath.Join(out, "tampered")
	if _, err := lease.Install(tampered); err == nil {
		t.Fatal("a cached executable rewritten after publication was handed out")
	}
	if _, err := os.Stat(tampered); !os.IsNotExist(err) {
		t.Fatalf("a refused copy was left behind: %v", err)
	}
}

func TestEntryWithoutPublicationRecordIsAMiss(t *testing.T) {
	entry := testEntry(t, t.TempDir(), "44444444-44444444")
	if err := os.MkdirAll(entry.entryDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry.binaryPath(), []byte("planted"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, found, err := entry.Acquire(context.Background()); err != nil || found {
		t.Fatalf("unrecorded executable served as a hit: found=%t err=%v", found, err)
	}
	lease, published, err := entry.AcquireOrMaterialize(context.Background(), func(path string) error {
		return os.WriteFile(path, []byte("compiled"), 0o755)
	})
	if err != nil || !published {
		t.Fatalf("unrecorded entry not replaced: published=%t err=%v", published, err)
	}
	t.Cleanup(func() {
		if err := lease.Release(); err != nil {
			t.Error(err)
		}
	})
	binary, err := lease.Install(filepath.Join(t.TempDir(), "verified"))
	if err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(binary.Path()); err != nil || string(body) != "compiled" {
		t.Fatalf("published executable = %q, %v", body, err)
	}
}
