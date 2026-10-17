//go:build unix

package controller_test

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

func burstSDKPipeService(t *testing.T, handler http.Handler) (string, []string) {
	t.Helper()
	directory := t.TempDir()
	paths := []string{filepath.Join(directory, "requests"), filepath.Join(directory, "responses")}
	files := make([]*os.File, 2)
	for i, path := range paths {
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		files[i] = file
		t.Cleanup(func() {
			if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				t.Error(err)
			}
		})
	}
	done := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(files[0])
		for {
			request, err := http.ReadRequest(reader)
			if err != nil {
				done <- err
				return
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request.WithContext(t.Context()))
			response := recorder.Result()
			response.ContentLength = int64(recorder.Body.Len())
			err = errors.Join(response.Write(files[1]), response.Body.Close(), request.Body.Close())
			if err != nil {
				done <- err
				return
			}
		}
	}()
	t.Cleanup(func() {
		for _, file := range files {
			if err := file.Close(); err != nil {
				t.Error(err)
			}
		}
		if err := <-done; !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	})
	return "http://127.0.0.1:1", paths
}

type burstSDKPipeTransport struct {
	mu       sync.Mutex
	requests *os.File
	reader   *bufio.Reader
}

func openBurstSDKPipeTransport(t *testing.T, requests, responses string) *burstSDKPipeTransport {
	t.Helper()
	requestFile, err := os.OpenFile(requests, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	responseFile, err := os.Open(responses)
	if err != nil {
		t.Fatal(err)
	}
	return &burstSDKPipeTransport{requests: requestFile, reader: bufio.NewReader(responseFile)}
}

func (p *burstSDKPipeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := errors.Join(request.Write(p.requests), request.Body.Close()); err != nil {
		return nil, err
	}
	response, err := http.ReadResponse(p.reader, request)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(response.Body)
	err = errors.Join(err, response.Body.Close())
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, err
}
