package launcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	ktesting "k8s.io/client-go/testing"
)

type pausedStopWatch struct {
	watch.Interface
	once    sync.Once
	stopped chan struct{}
	resume  <-chan struct{}
}

func (w *pausedStopWatch) Stop() {
	w.once.Do(func() {
		close(w.stopped)
		<-w.resume
		w.Interface.Stop()
	})
}

func TestLauncherPodWatchReconcileSurvivesReadyClaim(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, kube, _, base := fixture()
		l.Poll = time.Minute
		control := &applicationControl{controlFake: base, deliveries: make(chan *CloudJobAward)}
		l.Control = control
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		stream := watch.NewRaceFreeFake()
		resume := make(chan struct{}, 1)
		defer close(resume)
		pods := &pausedStopWatch{Interface: stream, stopped: make(chan struct{}), resume: resume}
		kube.PrependWatchReactor("pods", func(ktesting.Action) (bool, watch.Interface, error) { return true, pods, nil })
		var passes, publications atomic.Int32
		kube.PrependReactor("list", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
			if passes.Add(1) == 2 && publications.Load() != 1 {
				t.Error("pending reconcile ran before the ready award was applied")
			}
			return false, nil, nil
		})
		kube.PrependReactor("create", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
			publications.Add(1)
			return false, nil, nil
		})
		done := make(chan error, 1)
		go func() { done <- l.Run(ctx) }()
		synctest.Wait()
		if got := passes.Load(); got != 1 {
			t.Fatalf("initial reconciliation passes=%d want=1", got)
		}
		stream.Error(&metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonExpired, Code: 410})
		<-pods.stopped
		// safety: Stop proves the watch case was selected before a claim can become ready.
		control.deliveries <- fanOut(base.award, 1)[0]
		synctest.Wait()
		resume <- struct{}{}
		synctest.Wait()
		immediate := passes.Load()
		<-time.NewTimer(l.Poll / 2).C
		synctest.Wait()
		later := passes.Load()
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if publications.Load() != 1 || immediate != 2 || later != 2 {
			t.Fatalf("watch reconcile lost after claim: publications=%d immediate passes=%d after half poll=%d", publications.Load(), immediate, later)
		}
	})
}

func TestLauncherCoalescesPodWatchReplay(t *testing.T) {
	for _, reconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial replay", true: "reconnect replay"}[reconnect], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l, kube, _, control := fixture()
				control.award = nil
				l.Poll = 5 * time.Second
				first, replay := watch.NewRaceFreeFake(), watch.NewRaceFreeFake()
				for i := range 49 {
					replay.Add(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("pod-%d", i)}})
				}
				watches := 0
				kube.PrependWatchReactor("pods", func(ktesting.Action) (bool, watch.Interface, error) {
					watches++
					if reconnect && watches == 1 {
						return true, first, nil
					}
					return true, replay, nil
				})
				var passes atomic.Int32
				kube.PrependReactor("list", "jobs", func(ktesting.Action) (bool, runtime.Object, error) { passes.Add(1); return false, nil, nil })
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan error, 1)
				go func() { done <- l.Run(ctx) }()
				defer func() {
					cancel()
					if err := <-done; err != nil {
						t.Error(err)
					}
				}()
				synctest.Wait()
				if reconnect {
					first.Stop()
					synctest.Wait()
					before := passes.Load()
					time.Sleep(l.Poll)
					synctest.Wait()
					if got := passes.Load() - before; got != 1 {
						t.Fatalf("reconnect replay caused %d reconciles, want one", got)
					}
				} else if got := passes.Load(); got != 1 {
					t.Fatalf("initial replay caused %d reconciles, want one", got)
				}
			})
		})
	}
}

func TestLauncherReconnectsImmediatelyAfterCleanWatchTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, kube, _, control := fixture()
		control.award = nil
		l.Poll = 5 * time.Second
		first, second := watch.NewRaceFreeFake(), watch.NewRaceFreeFake()
		var watches atomic.Int32
		kube.PrependWatchReactor("pods", func(ktesting.Action) (bool, watch.Interface, error) {
			if watches.Add(1) == 1 {
				return true, first, nil
			}
			return true, second, nil
		})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- l.Run(ctx) }()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}()
		synctest.Wait()
		time.Sleep(podWatchTimeout)
		synctest.Wait()
		first.Stop()
		synctest.Wait()
		if got := watches.Load(); got != 2 {
			t.Fatalf("watches after clean timeout=%d, want immediate reconnect", got)
		}
	})
}

func TestLauncherBacksOffAfterWatchFailure(t *testing.T) {
	for _, mode := range []string{"error", "early close"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l, kube, _, control := fixture()
				control.award = nil
				l.Poll = 5 * time.Second
				first, second := watch.NewRaceFreeFake(), watch.NewRaceFreeFake()
				var watches atomic.Int32
				kube.PrependWatchReactor("pods", func(ktesting.Action) (bool, watch.Interface, error) {
					if watches.Add(1) == 1 {
						return true, first, nil
					}
					return true, second, nil
				})
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan error, 1)
				go func() { done <- l.Run(ctx) }()
				defer func() {
					cancel()
					if err := <-done; err != nil {
						t.Error(err)
					}
				}()
				synctest.Wait()
				if mode == "error" {
					time.Sleep(podWatchTimeout)
					synctest.Wait()
					first.Error(&metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonInternalError, Code: 500, Message: "watch unavailable"})
				} else {
					first.Stop()
				}
				synctest.Wait()
				if got := watches.Load(); got != 1 {
					t.Fatalf("watches immediately after failure=%d, want 1 until poll", got)
				}
				time.Sleep(l.Poll - time.Nanosecond)
				synctest.Wait()
				if got := watches.Load(); got != 1 {
					t.Fatalf("watches before poll elapsed=%d, want 1", got)
				}
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				if got := watches.Load(); got != 2 {
					t.Fatalf("watches at poll boundary=%d, want 2", got)
				}
			})
		})
	}
}

type watchLogBuffer struct {
	mu   sync.Mutex
	text strings.Builder
}

func (b *watchLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.Write(p)
}

func (b *watchLogBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.text.String() }

func TestLauncherWarnsOncePerPodWatchFailureStreak(t *testing.T) {
	for _, mode := range []string{"forbidden", "transport", "stream error", "idle recovery", "list forbidden"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l, kube, _, control := fixture()
				control.award = nil
				l.Poll = time.Second
				var failing atomic.Bool
				failing.Store(true)
				healthy := watch.NewRaceFreeFake()
				if mode == "list forbidden" {
					kube.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
						options := action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
						if failing.Load() && options.Limit == 1 {
							return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("cannot list pods in namespace cloud"))
						}
						return false, nil, nil
					})
				}
				kube.PrependWatchReactor("pods", func(ktesting.Action) (bool, watch.Interface, error) {
					if !failing.Load() {
						return true, healthy, nil
					}
					forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("cannot watch pods in namespace cloud"))
					switch mode {
					case "forbidden":
						return true, nil, forbidden
					case "transport", "idle recovery":
						return true, nil, errors.New("watch transport unavailable")
					default:
						stream := watch.NewRaceFreeFake()
						stream.Error(&forbidden.ErrStatus)
						return true, stream, nil
					}
				})
				logs := &watchLogBuffer{}
				l.Logger = slog.New(slog.NewTextHandler(logs, nil))
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan error, 1)
				go func() { done <- l.Run(ctx) }()
				defer func() {
					cancel()
					if err := <-done; err != nil {
						t.Error(err)
					}
				}()
				synctest.Wait()
				time.Sleep(2 * l.Poll)
				synctest.Wait()
				if got := strings.Count(logs.String(), "level=WARN"); got != 1 {
					t.Fatalf("first failure streak warnings=%d: %s", got, logs.String())
				}
				failing.Store(false)
				time.Sleep(l.Poll)
				synctest.Wait()
				if mode == "idle recovery" {
					time.Sleep(l.Poll)
				} else {
					healthy.Add(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "healthy"}})
				}
				synctest.Wait()
				failing.Store(true)
				healthy.Stop()
				synctest.Wait()
				time.Sleep(2 * l.Poll)
				synctest.Wait()
				text := logs.String()
				if got := strings.Count(text, "level=WARN"); got != 2 {
					t.Fatalf("warnings after recovery and second failure=%d: %s", got, text)
				}
				verb := "watch"
				if mode == "list forbidden" {
					verb = "list"
				}
				for _, part := range []string{"namespace=cloud", "resource=pods", "verb=" + verb} {
					if !strings.Contains(text, part) {
						t.Fatalf("watch warning missing %q: %s", part, text)
					}
				}
				if (mode == "forbidden" || mode == "stream error" || mode == "list forbidden") && !strings.Contains(text, "grant "+verb+" on pods") {
					t.Fatalf("forbidden warning omits permission remedy: %s", text)
				}
			})
		})
	}
}

func TestLauncherWatchStartsFromFreshSnapshotAfterExpiration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, kube, _, control := fixture()
		control.award = nil
		l.Poll = time.Second
		var lists atomic.Int32
		kube.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
			options := action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
			if options.Limit != 1 {
				return false, nil, nil
			}
			if options.LabelSelector != "app.kubernetes.io/managed-by="+managedByLauncher {
				return true, nil, errors.New("watch snapshot selected unrelated Pods")
			}
			version := fmt.Sprintf("snapshot-%d", lists.Add(1))
			return true, &corev1.PodList{ListMeta: metav1.ListMeta{ResourceVersion: version}}, nil
		})
		first, second := watch.NewFake(), watch.NewFake()
		versions := make(chan string, 2)
		calls := 0
		kube.PrependWatchReactor("pods", func(action ktesting.Action) (bool, watch.Interface, error) {
			versions <- action.(ktesting.WatchAction).GetWatchRestrictions().ResourceVersion
			calls++
			if calls == 1 {
				return true, first, nil
			}
			return true, second, nil
		})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- l.Run(ctx) }()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}()
		synctest.Wait()
		if got := <-versions; got != "snapshot-1" {
			t.Fatalf("initial watch version=%q, want snapshot-1", got)
		}
		first.Error(&metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonExpired, Code: 410, Message: "resource version expired"})
		synctest.Wait()
		time.Sleep(l.Poll)
		synctest.Wait()
		if got := <-versions; got != "snapshot-2" {
			t.Fatalf("reconnected watch version=%q, want fresh snapshot-2", got)
		}
	})
}
