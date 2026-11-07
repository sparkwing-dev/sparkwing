package services

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"

	helperruntime "github.com/sparkwing-dev/sparkwing/internal/hosthelpers/runtime"
)

func helperContext(t *testing.T) context.Context {
	var mu sync.Mutex
	next := 0
	commands := map[string][]string{}
	ctx := t.Context()
	return helperruntime.WithCleanups(ctx, func(argv []string, _ string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		next++
		id := strconv.Itoa(next)
		commands[id] = argv
		return id, nil
	}, func(id string) error {
		mu.Lock()
		argv, ok := commands[id]
		mu.Unlock()
		if !ok {
			return fmt.Errorf("missing cleanup")
		}
		if err := helperruntime.Command(context.WithoutCancel(ctx), argv[0], argv[1:]...).Run(); err != nil {
			return err
		}
		mu.Lock()
		delete(commands, id)
		mu.Unlock()
		return nil
	})
}
