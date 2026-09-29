package sparkwing_test

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestRunWork_SpawnEachJoinsChildrenAfterGeneratorFailure(t *testing.T) {
	for _, failure := range []string{"empty id", "invalid job", "panic"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := sparkwing.NewWork()
				sparkwing.JobSpawnEach(w, []int{0, 1}, func(item int) (string, any) {
					if item == 0 {
						return "child", func(context.Context) error { return nil }
					}
					switch failure {
					case "empty id":
						return "", func(context.Context) error { return nil }
					case "invalid job":
						return "invalid", 42
					default:
						panic("generator failed")
					}
				})
				cleanup := make(chan struct{})
				started, stopped := 0, 0
				handler := sparkwing.SpawnHandlerFunc(func(ctx context.Context, _, _ string, _ sparkwing.Workable) (any, error) {
					started++
					<-ctx.Done()
					<-cleanup
					stopped++
					return nil, ctx.Err()
				})
				ctx, _ := newWorkCtx()
				ctx = sparkwingruntime.WithSpawnHandler(ctx, handler)
				result := make(chan error, 1)
				go func() {
					_, err := sparkwing.RunWork(ctx, w)
					result <- err
				}()
				synctest.Wait()
				var resultErr error
				returned := false
				select {
				case resultErr = <-result:
					returned = true
					if started != stopped {
						t.Errorf("RunWork returned while %d child handlers were still running", started-stopped)
					}
				default:
				}
				close(cleanup)
				if !returned {
					resultErr = <-result
				}
				synctest.Wait()
				if started != stopped {
					t.Errorf("RunWork left %d child handlers running", started-stopped)
				}
				if resultErr == nil {
					t.Fatal("generator failure was lost")
				}
				want := map[string]string{"empty id": "empty id", "invalid job": "unsupported type", "panic": "generator failed"}[failure]
				if !strings.Contains(resultErr.Error(), want) {
					t.Errorf("error = %v, want generator failure %q", resultErr, want)
				}
			})
		})
	}
}
