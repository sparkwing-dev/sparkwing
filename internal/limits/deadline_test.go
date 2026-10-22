package limits

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"
)

func TestChildBudget(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, tc := range []struct {
		name         string
		parent       time.Time
		want, margin time.Duration
		end          time.Time
		bad          bool
	}{
		{"unbounded", time.Time{}, 0, 0, time.Time{}, false},
		{"own budget", time.Time{}, time.Minute, time.Second, now.Add(time.Minute), false},
		{"remaining", now.Add(time.Minute), 0, time.Second, now.Add(59 * time.Second), false},
		{"shorter", now.Add(time.Minute), time.Second, time.Second, now.Add(time.Second), false},
		{"clipped", now.Add(time.Minute), time.Hour, time.Second, now.Add(59 * time.Second), false},
		{"expired", now, time.Second, 0, time.Time{}, true},
		{"margin exhausted", now.Add(time.Second), 0, time.Second, time.Time{}, true},
		{"negative margin", time.Time{}, 0, -1, time.Time{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Child(now, tc.parent, tc.want, tc.margin)
			if (err != nil) != tc.bad || !got.Equal(tc.end) {
				t.Fatalf("got %v, %v; want %v bad=%v", got, err, tc.end, tc.bad)
			}
		})
	}
	_, err := Child(now, now.Add(-1), 0, 0)
	if !errors.Is(err, ErrNoTime) {
		t.Fatal(err)
	}
}

func TestChildContextPreservesCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	child, done, err := ChildContext(parent, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if _, ok := child.Deadline(); ok {
		t.Fatal("unexpected deadline")
	}
	cancel()
	if !errors.Is(child.Err(), context.Canceled) {
		t.Fatal(child.Err())
	}
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		bounded, stop := context.WithDeadline(context.Background(), now.Add(time.Hour))
		defer stop()
		child, done, err := ChildContext(bounded, time.Minute, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer done()
		end, ok := child.Deadline()
		if !ok || !end.Equal(now.Add(time.Minute)) {
			t.Fatal("missing child bound")
		}
	})
	_, _, err = ChildContext(context.Background(), 0, -1)
	if err == nil {
		t.Fatal("negative margin accepted")
	}
}

func TestBoundedText(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bound int
		fn    func(string) string
	}{{"error", int(ErrorBytes), Error}, {"record", int(AnnotationBytes), Record}} {
		t.Run(tc.name, func(t *testing.T) {
			if text := "summary: [MASKED] ✓"; tc.fn(text) != text {
				t.Fatal("changed in-budget UTF-8 text")
			}
			for _, n := range []int{0, tc.bound - 1, tc.bound, tc.bound + 1} {
				s := strings.Repeat("x", n)
				got := tc.fn(s)
				if len(got) > tc.bound || !utf8.ValidString(got) {
					t.Fatal("invalid bounded text")
				}
				if n <= tc.bound && got != s {
					t.Fatal("changed in-budget text")
				}
			}
			for _, unit := range []string{"x", "é", "界", "🐦"} {
				s := "HEAD [MASKED]" + strings.Repeat(unit, tc.bound) + "TAIL [MASKED]"
				got := tc.fn(s)
				if len(got) > tc.bound || !utf8.ValidString(got) || !strings.HasPrefix(got, "HEAD [MASKED]") || !strings.HasSuffix(got, "TAIL [MASKED]") || !strings.Contains(got, "elided") {
					t.Fatal("lost head/tail or invalid UTF-8")
				}
			}
		})
	}
	for _, n := range []int{int(failureBytes) - 1, int(failureBytes), int(failureBytes) + 1} {
		raw := []byte(strings.Repeat("x", n))
		got, err := Failure(raw)
		if (err != nil) != (n > int(failureBytes)) {
			t.Fatal("wrong failure bound")
		}
		if err == nil && string(got) != string(raw) {
			t.Fatal("structured record changed")
		}
		if err != nil && got != nil {
			t.Fatal("returned truncated structured record")
		}
	}
}
