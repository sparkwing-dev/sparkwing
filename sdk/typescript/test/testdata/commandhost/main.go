package main

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var req nodeproto.ExecRequest
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		panic(err)
	}
	var mu sync.Mutex
	encoder := json.NewEncoder(os.Stdout)
	done := nodehost.ExecuteCommand(ctx, req, func(stream string, data []byte, eof bool) error {
		if eof {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		return encoder.Encode(nodeproto.ExecOutput{ID: req.ID, Stream: stream, Data: data})
	})
	if err := encoder.Encode(done); err != nil {
		panic(err)
	}
}
