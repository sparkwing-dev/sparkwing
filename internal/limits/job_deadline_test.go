package limits

import (
	"errors"
	"testing"
	"time"
)

func TestJobDeadline(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name                 string
		owner, attempt, want time.Time
		expired              bool
	}{
		{name: "unbounded"},
		{name: "owner only", owner: now.Add(time.Hour), want: now.Add(time.Hour)},
		{name: "attempt slack", attempt: now.Add(time.Minute), want: now.Add(time.Minute + JobSlack)},
		{name: "owner caps slack", owner: now.Add(5 * time.Minute), attempt: now.Add(time.Minute), want: now.Add(5 * time.Minute)},
		{name: "expired owner", owner: now, expired: true},
		{name: "expired attempt", owner: now.Add(time.Hour), attempt: now, expired: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := JobDeadline(now, tt.owner, tt.attempt)
			if tt.expired {
				if !errors.Is(err, ErrNoTime) {
					t.Fatalf("expiry=%v", err)
				}
				return
			}
			if err != nil || !got.Equal(tt.want) {
				t.Fatalf("deadline=%v,%v; want %v", got, err, tt.want)
			}
		})
	}
}
