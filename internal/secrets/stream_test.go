package secrets

import (
	"strings"
	"testing"
)

const streamSecret = "hunter2-very-secret"

const pieceLen = 1 << 20

func TestStreamMasksASecretSplitAcrossPieces(t *testing.T) {
	m := NewMasker()
	m.Register(streamSecret)
	s := m.Stream()
	head := strings.Repeat("x", pieceLen-4)
	got := s.Mask(head+streamSecret[:4], false) + s.Mask(streamSecret[4:]+" tail", true)
	if want := head + "*** tail"; got != want {
		t.Fatalf("pieces masked as %q...%q, want the secret masked whole", got[:8], got[len(head)-8:])
	}
	long := strings.Repeat("y", pieceLen)
	if got := s.Mask(long, false) + s.Mask("", true); got != long {
		t.Fatalf("held tail of an unfinished line lost: got %d bytes, want %d", len(got), len(long))
	}
}

func TestStreamHoldsAtMostOneValueOfOverlappingText(t *testing.T) {
	m := NewMasker()
	m.Register("aa")
	s := m.Stream()
	run := strings.Repeat("a", pieceLen)
	for i := range 3 {
		got := s.Mask(run, false)
		if len(got) < pieceLen-8 || strings.Contains(got, "aa") {
			t.Fatalf("piece %d shown as %d bytes, holding back too much or leaking", i, len(got))
		}
	}
	if got := s.Mask("", true); strings.Contains(got, "aa") {
		t.Fatalf("overlapping secret shown unmasked: %q", got)
	}
}

func TestNilMaskerStreamPassesTextThrough(t *testing.T) {
	var m *Masker
	s := m.Stream()
	if got := s.Mask("abc", false) + s.Mask("def", true); got != "abcdef" {
		t.Fatalf("got %q", got)
	}
}

func TestStreamBoundsChainsOfOverlappingMatches(t *testing.T) {
	m := newTestMasker("abab", "baba")
	stream := m.Stream()
	for range 4 {
		stream.Mask(strings.Repeat("ab", 1<<14), false)
		if len(stream.held) > 64 {
			t.Fatal("overlap retained an unbounded line")
		}
	}
	if got := stream.Mask(" tail", true); strings.Contains(got, "abab") || strings.Contains(got, "baba") {
		t.Fatal("overlap tail exposed a registered value")
	}
}
