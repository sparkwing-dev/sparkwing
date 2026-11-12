package lease

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestKeeperDiagnosticsReportActualShorterExpiryAndReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var events []Event
		diagnostics := NewDiagnostics(t.Context(), func(_ context.Context, event Event) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, event)
			return nil
		})
		defer diagnostics.Close()
		start := time.Now()
		calls := 0
		keeper := Keeper{Lease: time.Minute, Interval: 10 * time.Second, Diagnostics: diagnostics, Identity: Identity{Role: "OWNER", Run: "r"}, Renew: func(ctx context.Context) (Beat, error) {
			calls++
			if calls == 1 {
				return Beat{ExpiresAt: start.Add(12 * time.Second)}, nil
			}
			<-ctx.Done()
			return Beat{}, ctx.Err()
		}}
		ctx, stop := keeper.Run(t.Context())
		<-ctx.Done()
		stop()
		synctest.Wait()
		if time.Since(start) != 12*time.Second {
			t.Fatalf("expiry changed: %v", time.Since(start))
		}
		mu.Lock()
		defer mu.Unlock()
		var selected, reset, end bool
		for _, e := range events {
			if e.Kind == "expiry_selected" {
				selected = e.ExpiresAt.Equal(start.Add(12 * time.Second))
			}
			if e.Kind == "watch_reset" {
				reset = e.ExpiresAt.Equal(start.Add(12 * time.Second))
			}
			if e.Kind == "execution_end" {
				end = true
			}
			if e.Identity.Role != "OWNER" || e.Instance == "" || e.Sequence == 0 {
				t.Fatalf("unbound event: %+v", e)
			}
		}
		if !selected || !reset || !end {
			t.Fatalf("missing actual expiry events: %+v", events)
		}
	})
}

func TestKeeperDiagnosticsCannotDelayRenewalOrStop(t *testing.T) {
	for _, mode := range []string{"absent", "error", "panic", "blocked"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				var diagnostics *Diagnostics
				if mode != "absent" {
					diagnostics = NewDiagnostics(t.Context(), func(context.Context, Event) error {
						switch mode {
						case "blocked":
							<-release
						case "panic":
							panic("sink failed")
						}
						return errors.New("sink unavailable")
					})
					defer diagnostics.Close()
				}
				calls, observed := 0, 0
				keeper := Keeper{Lease: time.Minute, Interval: time.Second, Diagnostics: diagnostics, Renew: func(context.Context) (Beat, error) { calls++; return Beat{}, nil }, Observe: func(Beat) { observed++ }}
				ctx, stop := keeper.Run(t.Context())
				synctest.Wait()
				time.Sleep(3 * time.Second)
				synctest.Wait()
				stop()
				if calls != 4 || observed != 4 || !errors.Is(context.Cause(ctx), context.Canceled) {
					t.Fatalf("diagnostics changed behavior: calls=%d observed=%d cause=%v", calls, observed, context.Cause(ctx))
				}
				close(release)
				synctest.Wait()
			})
		})
	}
}

func TestKeeperDiagnosticsConcurrentRolesAndInheritedDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var events []Event
		diagnostics := NewDiagnostics(t.Context(), func(_ context.Context, e Event) error { events = append(events, e); return nil })
		defer diagnostics.Close()
		parent, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		var stops []func()
		for _, role := range []string{"OWNER", "NODE"} {
			keeper := Keeper{Lease: time.Minute, Interval: time.Second, Diagnostics: diagnostics, Identity: Identity{Role: role, Run: "r"}, Renew: func(context.Context) (Beat, error) { return Beat{}, nil }}
			_, stop := keeper.Run(parent)
			stops = append(stops, stop)
		}
		<-parent.Done()
		for _, stop := range stops {
			stop()
		}
		synctest.Wait()
		roles := map[string]string{}
		seq := map[string]uint64{}
		deadline, _ := parent.Deadline()
		for _, e := range events {
			roles[e.Identity.Role] = e.Instance
			if e.Sequence != seq[e.Instance]+1 || e.Dropped != 0 || !e.Deadline.Equal(deadline) || e.MonoAfter < e.MonoBefore {
				t.Fatalf("invalid ordered evidence: %+v", e)
			}
			seq[e.Instance] = e.Sequence
		}
		if len(roles) != 2 || roles["OWNER"] == roles["NODE"] {
			t.Fatalf("keepers conflated: %v", roles)
		}
	})
}

func TestKeeperDiagnosticsResetReportsInflightRenewalAndLoss(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		var events []Event
		diagnostics := NewDiagnostics(t.Context(), func(_ context.Context, e Event) error { events = append(events, e); return nil })
		defer diagnostics.Close()
		calls := 0
		keeper := Keeper{Lease: time.Minute, Interval: time.Second, Diagnostics: diagnostics, Renew: func(ctx context.Context) (Beat, error) {
			calls++
			if calls == 2 {
				close(started)
				<-release
			}
			return Beat{}, nil
		}}
		_, stop := keeper.Run(t.Context())
		<-started
		synctest.Wait()
		// The watcher stays armed while renewal is blocked; expiry still cancels the unit.
		time.Sleep(time.Minute)
		synctest.Wait()
		close(release)
		stop()
		synctest.Wait()
		var end bool
		for _, e := range events {
			if e.Kind == "execution_end" {
				end = e.InFlight
			}
		}
		if !end {
			t.Fatalf("execution end lost in-flight renewal: %+v", events)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var events []Event
		diagnostics := NewDiagnostics(t.Context(), func(_ context.Context, e Event) error { <-release; events = append(events, e); return nil })
		defer diagnostics.Close()
		calls := 0
		keeper := Keeper{Lease: time.Minute, Interval: time.Second, Diagnostics: diagnostics, Renew: func(context.Context) (Beat, error) { calls++; return Beat{}, nil }}
		_, stop := keeper.Run(t.Context())
		synctest.Wait()
		time.Sleep(100 * time.Second)
		synctest.Wait()
		close(release)
		synctest.Wait()
		time.Sleep(time.Second)
		stop()
		synctest.Wait()
		var lost bool
		for _, e := range events {
			if e.Dropped > 0 {
				lost = true
			}
		}
		if !lost || calls < 100 {
			t.Fatalf("loss invisible or affected renewals: events=%d calls=%d", len(events), calls)
		}
	})
}

func TestKeeperDiagnosticsNominalHourVolume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var events []Event
		diagnostics := NewDiagnostics(t.Context(), func(_ context.Context, e Event) error { events = append(events, e); return nil })
		defer diagnostics.Close()
		keeper := Keeper{Lease: 3 * time.Minute, Interval: time.Minute, Identity: Identity{Role: "OWNER", Run: "run-volume", Generation: 1}, Diagnostics: diagnostics, Renew: func(context.Context) (Beat, error) { return Beat{}, nil }}
		_, stop := keeper.Run(t.Context())
		synctest.Wait()
		time.Sleep(time.Hour - time.Nanosecond)
		stop()
		synctest.Wait()
		size := 0
		var framed bytes.Buffer
		for _, e := range events {
			raw, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			size += len(raw) + 1
			if err := json.NewEncoder(&framed).Encode(struct {
				TS    string          `json:"ts"`
				Level string          `json:"level"`
				Event string          `json:"event"`
				Attrs json.RawMessage `json:"attrs"`
			}{e.Wall.Format(time.RFC3339Nano), "debug", "lease_keeper", raw}); err != nil {
				t.Fatal(err)
			}
			if e.Dropped != 0 {
				t.Fatal("volume sample dropped events")
			}
		}
		if len(events) != 242 {
			t.Fatalf("one-hour events=%d, want 242", len(events))
		}
		var compressed bytes.Buffer
		z := gzip.NewWriter(&compressed)
		if _, err := z.Write(framed.Bytes()); err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		t.Logf("60 renewals, %d events, %d event JSONL bytes, %d framed JSONL bytes, %d gzip bytes per keeper-hour", len(events), size, framed.Len(), compressed.Len())
	})
}

func TestKeeperDiagnosticsWatchResetIncludesInflightState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var events []Event
		diagnostics := NewDiagnostics(t.Context(), func(_ context.Context, e Event) error { events = append(events, e); return nil })
		defer diagnostics.Close()
		trace := diagnostics.start(t.Context(), Identity{Role: "NODE", Run: "r"})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		renewal := make(chan leaseUpdate)
		done := make(chan struct{})
		go func() {
			defer close(done)
			watch(ctx, func(error) { cancel() }, renewal, time.Now().Add(time.Minute), trace)
		}()
		synctest.Wait()
		trace.emit("renewal_start", time.Now(), time.Time{}, "", 0)
		renewal <- leaseUpdate{expires: time.Now().Add(20 * time.Second), renewal: 1}
		synctest.Wait()
		cancel()
		<-done
		synctest.Wait()
		var reset bool
		for _, e := range events {
			if e.Kind == "watch_reset" {
				reset = e.InFlight && e.Renewal == 1
			}
		}
		if !reset {
			t.Fatalf("reset lost in-flight state: %+v", events)
		}
	})
}

func TestKeeperWatchResetIdentifiesInstalledRenewal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var events []Event
		diagnostics := NewDiagnostics(t.Context(), func(_ context.Context, e Event) error { events = append(events, e); return nil })
		defer diagnostics.Close()
		trace := diagnostics.start(t.Context(), Identity{Role: "NODE", Run: "r"})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		renewal := make(chan leaseUpdate)
		done := make(chan struct{})
		go func() {
			defer close(done)
			watch(ctx, func(error) { cancel() }, renewal, time.Now().Add(time.Minute), trace)
		}()
		synctest.Wait()
		trace.emit("renewal_start", time.Now(), time.Time{}, "", 0)
		trace.emit("renewal_finish", time.Time{}, time.Time{}, "success", 0)
		trace.emit("renewal_start", time.Now(), time.Time{}, "", 0)
		renewal <- leaseUpdate{expires: time.Now().Add(20 * time.Second), renewal: 1}
		synctest.Wait()
		cancel()
		<-done
		synctest.Wait()
		for _, e := range events {
			if e.Kind == "watch_reset" {
				raw, err := json.Marshal(e)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]any
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				if fields["watch_renewal"] != float64(1) || e.Renewal != 2 || !e.InFlight {
					t.Fatalf("reset conflated installed renewal with in-flight renewal: %s", raw)
				}
				return
			}
		}
		t.Fatal("no reset")
	})
}
