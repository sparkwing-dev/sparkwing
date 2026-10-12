package logseg_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
)

func TestAttemptPrefixLayout(t *testing.T) {
	got, err := logseg.AttemptPrefix(logseg.ClassFree, "acme", "run-1", "build/linux", 2, 7)
	if err != nil {
		t.Fatal(err)
	}
	if want := "logs/r30/teams/acme/runs/run-1/build_2flinux/a2-g7/"; got != want {
		t.Fatalf("prefix %q, want %q", got, want)
	}
	other, err := logseg.AttemptPrefix(logseg.ClassFree, "acme", "run-1", "build__linux", 2, 7)
	if err != nil || other == got {
		t.Fatalf("hierarchical and flat node IDs share a prefix: %q, %v", other, err)
	}
	if err := storage.SafeArtifactKey(logseg.SegmentKey(got, 1)); err != nil {
		t.Fatalf("segment key is not a safe artifact key: %v", err)
	}
	if esc := logseg.EscapeNode(".hidden/a_b c"); esc != "_2ehidden_2fa_5fb_20c" {
		t.Fatalf("EscapeNode = %q", esc)
	}
	if key := logseg.SegmentKey(got, 12); key != got+"seg-000012.ndjson.gz" {
		t.Fatalf("segment key %q", key)
	}
	if n, ok := logseg.SegmentNumber(got, logseg.SegmentKey(got, 12)); !ok || n != 12 {
		t.Fatalf("SegmentNumber = %d, %v", n, ok)
	}
	if _, ok := logseg.SegmentNumber(got, logseg.ManifestKey(got)); ok {
		t.Fatal("the manifest parsed as a segment")
	}
}

func TestAttemptPrefixRefusesBadParts(t *testing.T) {
	for name, args := range map[string]struct {
		class, team, run, node string
		attempt                int64
	}{
		"class":   {"90", "acme", "r", "n", 1},
		"team":    {logseg.ClassStandard, "Acme/../x", "r", "n", 1},
		"run":     {logseg.ClassStandard, "acme", "../r", "n", 1},
		"node":    {logseg.ClassStandard, "acme", "r", "a/../b", 1},
		"attempt": {logseg.ClassStandard, "acme", "r", "n", 0},
	} {
		if _, err := logseg.AttemptPrefix(args.class, args.team, args.run, args.node, args.attempt, 1); err == nil {
			t.Errorf("%s: accepted %+v", name, args)
		}
	}
	if _, err := logseg.AttemptPrefix(logseg.ClassStandard, logseg.NoTeam, "r", "n", 1, 0); err != nil {
		t.Fatalf("teamless run refused: %v", err)
	}
}

func TestFindAttemptsListsUnrecordedLogs(t *testing.T) {
	objs := newMemObjects()
	for _, a := range []struct {
		class, node string
		attempt     int64
	}{{logseg.ClassStandard, "build/linux", 1}, {logseg.ClassStandard, "build/linux", 2}, {logseg.ClassFree, "test", 1}} {
		p, err := logseg.AttemptPrefix(a.class, "acme", "run-1", a.node, a.attempt, 1)
		if err != nil {
			t.Fatal(err)
		}
		objs.put(logseg.ManifestKey(p), []byte("{}"))
	}
	classes := []string{logseg.ClassStandard, logseg.ClassFree}
	all, err := logseg.FindAttempts(t.Context(), objs, classes, "acme", "run-1", "")
	if err != nil || len(all) != 3 || all[0].NodeID != "build/linux" || all[1].Attempt != 2 || all[2].NodeID != "test" {
		t.Fatalf("attempts = %+v, %v", all, err)
	}
	one, err := logseg.FindAttempts(t.Context(), objs, classes, "acme", "run-1", "test")
	if err != nil || len(one) != 1 || !strings.HasPrefix(one[0].Prefix, "logs/r30/") {
		t.Fatalf("attempts of test = %+v, %v", one, err)
	}
	if node, ok := logseg.UnescapeNode(logseg.EscapeNode(".x/a_b")); !ok || node != ".x/a_b" {
		t.Fatalf("UnescapeNode round trip = %q, %v", node, ok)
	}
}
