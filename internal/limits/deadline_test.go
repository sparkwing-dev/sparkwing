package limits

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
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
	bounded, stop := context.WithDeadline(context.Background(), time.Now().Add(time.Hour))
	defer stop()
	child, done, err = ChildContext(bounded, time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	end, ok := child.Deadline()
	if !ok || time.Until(end) > time.Minute {
		t.Fatal("missing child bound")
	}
	_, _, err = ChildContext(context.Background(), 0, -1)
	if err == nil {
		t.Fatal("negative margin accepted")
	}
}

func TestRenewalStrictBoundary(t *testing.T) {
	if Renew(10*time.Second) != 10*time.Second/3 {
		t.Fatal("wrong cadence")
	}
	for _, tc := range []struct {
		every, ttl time.Duration
		good       bool
	}{{1, 3, true}, {1, 2, false}, {0, 3, false}, {1, 0, false}, {-1, 3, false}, {time.Duration(math.MaxInt64), time.Duration(math.MaxInt64), false}, {time.Duration(math.MaxInt64 / 3), time.Duration(math.MaxInt64), true}} {
		if err := CheckRenewal(tc.every, tc.ttl); (err == nil) != tc.good {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func TestJobDeadline(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, tc := range []struct {
		owner, attempt, want time.Time
		bad                  bool
	}{
		{time.Time{}, time.Time{}, time.Time{}, false},
		{now.Add(time.Hour), time.Time{}, now.Add(time.Hour), false},
		{time.Time{}, now.Add(time.Minute), now.Add(time.Minute + JobSlack), false},
		{now.Add(time.Minute), now.Add(time.Minute), now.Add(time.Minute), false},
		{now, now.Add(time.Minute), time.Time{}, true},
		{now.Add(time.Hour), now, time.Time{}, true},
	} {
		got, err := JobDeadline(now, tc.owner, tc.attempt)
		if (err != nil) != tc.bad || !got.Equal(tc.want) {
			t.Fatalf("%+v: %v %v", tc, got, err)
		}
	}
}

func TestBoundedText(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bound int
		fn    func(string) string
	}{{"error", int(ErrorBytes), Error}, {"record", int(AnnotationBytes), Record}} {
		t.Run(tc.name, func(t *testing.T) {
			for _, n := range []int{tc.bound - 1, tc.bound, tc.bound + 1} {
				s := strings.Repeat("x", n)
				got := tc.fn(s)
				if len(got) > tc.bound || !utf8.ValidString(got) {
					t.Fatal("invalid bounded text")
				}
				if n <= tc.bound && got != s {
					t.Fatal("changed in-budget text")
				}
			}
			s := "HEAD" + strings.Repeat("😀", tc.bound) + "TAIL"
			got := tc.fn(s)
			if len(got) > tc.bound || !utf8.ValidString(got) || !strings.HasPrefix(got, "HEAD") || !strings.HasSuffix(got, "TAIL") || !strings.Contains(got, "elided") {
				t.Fatal("lost head/tail or invalid UTF-8")
			}
		})
	}
	for _, n := range []int{int(FailureBytes) - 1, int(FailureBytes), int(FailureBytes) + 1} {
		raw := []byte(strings.Repeat("x", n))
		got, err := Failure(raw)
		if (err != nil) != (n > int(FailureBytes)) {
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
