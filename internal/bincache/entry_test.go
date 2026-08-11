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

func TestCacheDiscoveryRejectsEffectivelyUnboundedCallerLimits(t *testing.T) {
	if got := boundedCacheDiscoveryLimit(math.MaxInt); got != maxCacheDiscoveryEntries {
		t.Fatalf("boundedCacheDiscoveryLimit(MaxInt) = %d, want %d", got, maxCacheDiscoveryEntries)
	}
	if got := boundedCacheDiscoveryLimit(7); got != 7 {
		t.Fatalf("boundedCacheDiscoveryLimit(7) = %d, want 7", got)
	}
}

func seedEntry(t *testing.T, entry Entry, body string, modTime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(entry.binaryPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry.binaryPath(), []byte(body), 0o755); err != nil {
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
		Root:         root,
		ReclaimBytes: 1,
		MaxEntries:   2,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if result.Active != 1 || result.Reclaimed != 1 || result.ReclaimedBytes != 5 || !result.GoalSatisfied {
		t.Fatalf("unexpected result: %+v", result)
	}
	if _, err := os.Stat(old.binaryPath()); err != nil {
		t.Fatalf("active entry removed: %v", err)
	}
	if _, err := os.Stat(newer.binaryPath()); !os.IsNotExist(err) {
		t.Fatalf("reclaimable entry remains: %v", err)
	}
}

func TestPruneIsBoundedAndUsesStableKeyOrder(t *testing.T) {
	root := t.TempDir()
	stamp := time.Unix(1, 0)
	first := testEntry(t, root, "11111111-11111111")
	second := testEntry(t, root, "22222222-22222222")
	third := testEntry(t, root, "33333333-33333333")
	seedEntry(t, third, "333", stamp)
	seedEntry(t, second, "22", stamp)
	seedEntry(t, first, "1", stamp)

	result, err := Prune(context.Background(), PruneOptions{
		Root:         root,
		ReclaimBytes: 100,
		MaxEntries:   1,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if result.Examined != 1 || result.Reclaimed != 1 || result.ObservedBytes != 1 || result.ReclaimedBytes != 1 {
		t.Fatalf("unexpected bounded accounting: %+v", result)
	}
	if !result.Exhausted || result.GoalSatisfied {
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

	first, err := Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
	if err != nil || first.Reclaimed != 1 {
		t.Fatalf("first Prune = (%+v, %v)", first, err)
	}
	second, err := Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatalf("second Prune: %v", err)
	}
	if second.Reclaimed != 0 || !second.Exhausted {
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

	result, err := Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatalf("Prune while writer active: %v", err)
	}
	if result.Busy != 1 || result.Reclaimed != 0 {
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
		{Root: root, ReclaimBytes: 0, MaxEntries: 1},
		{Root: root, ReclaimBytes: 1, MaxEntries: 0},
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

	result, err := Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 100, MaxEntries: 2})
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
	if result.ObservedBytes != 11 || result.ReclaimedBytes != 0 || result.Reclaimed != 0 {
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

	result, err := Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatalf("Prune with returned lease: %v", err)
	}
	if result.Active != 1 || result.Reclaimed != 0 {
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

func TestStatusMeasuresManagedLivenessAndLegacyBytes(t *testing.T) {
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
	if status.ObservedBytes != 8 || status.EntryCount != 2 || status.ActiveEntries != 1 || status.ActiveBytes != 3 {
		t.Fatalf("managed status: %+v", status)
	}
	if status.LegacyBytes != 7 || status.LegacyEntries != 1 {
		t.Fatalf("legacy status: %+v", status)
	}
}

func TestPruneRetiresLegacyEntriesAutomatically(t *testing.T) {
	originalNow := cacheNow
	t.Cleanup(func() { cacheNow = originalNow })
	now := time.Unix(100, 0)
	cacheNow = func() time.Time { return now }
	cacheRoot := t.TempDir()
	root := filepath.Join(cacheRoot, pipelineCacheSchema)
	legacyDir := filepath.Join(cacheRoot, "11111111-11111111")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "pipelines"), []byte("legacy"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reclaimed != 0 || result.GoalSatisfied {
		t.Fatalf("first legacy prune must quarantine without claiming bytes: %+v", result)
	}
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatalf("legacy entry remains in its live namespace: %v", err)
	}
	quarantine := filepath.Join(root, "legacy-retired", "11111111-11111111")
	if _, err := os.Stat(quarantine); err != nil {
		t.Fatalf("quarantined entry: %v", err)
	}

	now = now.Add(legacyRetirementGrace)
	result, err = Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.ReclaimedBytes <= 0 || result.Reclaimed != 1 || !result.GoalSatisfied {
		t.Fatalf("mature legacy prune result = %+v", result)
	}
	if _, err := os.Stat(quarantine); !os.IsNotExist(err) {
		t.Fatalf("mature quarantine remains: %v", err)
	}
}

func TestPruneQuarantinesActiveLegacyWriterUntilGraceExpires(t *testing.T) {
	originalNow := cacheNow
	t.Cleanup(func() { cacheNow = originalNow })
	now := time.Unix(100, 0)
	cacheNow = func() time.Time { return now }
	cacheRoot := t.TempDir()
	root := filepath.Join(cacheRoot, pipelineCacheSchema)
	legacyDir := filepath.Join(cacheRoot, "11111111-11111111")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writer, err := os.OpenFile(filepath.Join(legacyDir, "pipelines"), os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	if _, err := writer.WriteString("before"); err != nil {
		t.Fatal(err)
	}

	if _, err := Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1}); err != nil {
		t.Fatal(err)
	}
	quarantine := filepath.Join(root, "legacy-retired", "11111111-11111111")
	if _, err := writer.WriteString("after"); err != nil {
		t.Fatalf("quarantine interrupted active writer: %v", err)
	}
	now = now.Add(legacyRetirementGrace - time.Second)
	if _, err := Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(quarantine); err != nil {
		t.Fatalf("active writer quarantine retired before grace: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	result, err := Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reclaimed != 1 {
		t.Fatalf("closed writer quarantine was not reclaimed: %+v", result)
	}
}

func TestDeferredLegacyDoesNotStarveManagedPressureReclaim(t *testing.T) {
	originalNow := cacheNow
	t.Cleanup(func() { cacheNow = originalNow })
	now := time.Unix(100, 0)
	cacheNow = func() time.Time { return now }
	cacheRoot := t.TempDir()
	root := filepath.Join(cacheRoot, pipelineCacheSchema)
	managed := testEntry(t, root, "22222222-22222222")
	seedEntry(t, managed, "managed", time.Unix(1, 0))
	retired := filepath.Join(root, "legacy-retired", "11111111-11111111")
	if err := os.MkdirAll(retired, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retired, "pipelines"), []byte("legacy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(retired, now, now); err != nil {
		t.Fatal(err)
	}

	result, err := Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reclaimed != 1 || result.ReclaimedBytes != 7 || !result.GoalSatisfied {
		t.Fatalf("managed pressure reclaim was starved: %+v", result)
	}
	if _, err := os.Stat(managed.entryDir()); !os.IsNotExist(err) {
		t.Fatalf("managed entry remains: %v", err)
	}
	if _, err := os.Stat(retired); err != nil {
		t.Fatalf("deferred legacy entry changed: %v", err)
	}
}
