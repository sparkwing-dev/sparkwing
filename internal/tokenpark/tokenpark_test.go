package tokenpark

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestFileChanged_SeesARewriteAndNothingElse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("token: old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := FileChanged(path)
	if changed() {
		t.Fatal("an untouched file reported a change")
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if !changed() {
		t.Fatal("a new mtime was not reported as a change")
	}
}

func TestFileChanged_SeesAFileAppearOrVanish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	appeared := FileChanged(path)
	if appeared() {
		t.Fatal("a file still absent reported a change")
	}
	if err := os.WriteFile(path, []byte("token: new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !appeared() {
		t.Fatal("a file that appeared was not reported")
	}
	vanished := FileChanged(path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !vanished() {
		t.Fatal("a file that vanished was not reported")
	}
}

func TestWait_EndsAtTheDeadlineOrTheChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		if Wait(t.Context(), time.Hour, func() bool { return false }) {
			t.Fatal("Wait reported a change nobody made")
		}
		if got := time.Since(start); got != time.Hour {
			t.Fatalf("an unchanged wait took %s, want the full hour", got)
		}

		start = time.Now()
		var flipped atomic.Bool
		// safety: the change lands between watch ticks, so the tick after it is the one that must see it.
		time.AfterFunc(time.Minute+2*time.Second, func() { flipped.Store(true) })
		if !Wait(t.Context(), time.Hour, flipped.Load) {
			t.Fatal("Wait missed the change")
		}
		if got := time.Since(start); got != time.Minute+WatchInterval {
			t.Fatalf("a change 62s in ended the wait after %s, want at the next watch tick", got)
		}

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		start = time.Now()
		if Wait(ctx, time.Hour, nil) || time.Since(start) != time.Second {
			t.Fatal("a cancelled context did not end the wait at once")
		}
	})
}
