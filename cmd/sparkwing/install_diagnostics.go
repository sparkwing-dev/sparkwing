package main

import (
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"io"
	"sync"
)

type installDiagnostics struct {
	mu     sync.Mutex
	out    io.Writer
	stream *secrets.Stream
	err    error
}

func (w *installDiagnostics) Write(body []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil {
		w.err = w.write(w.stream.Mask(string(body), false))
	}
	if w.err != nil {
		return 0, w.err
	}
	return len(body), nil
}

func (w *installDiagnostics) write(text string) error {
	n, err := io.WriteString(w.out, text)
	if err == nil && n != len(text) {
		err = io.ErrShortWrite
	}
	return err
}

func (w *installDiagnostics) close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil {
		w.err = w.write(w.stream.Mask("", true))
	}
	return w.err
}
