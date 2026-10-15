package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func streamDashboardLogs(ctx context.Context, inv clisurface.Invocation, openLog func(context.Context) (*os.File, error), out io.Writer, mode string) error {
	limit, err := dashboardLogLimit(inv)
	if err != nil {
		return err
	}
	if openLog == nil {
		return dashboardUnavailable("local controller log source")
	}
	file, err := openLog(ctx)
	if err != nil {
		return err
	}
	if file == nil {
		return dashboardUnavailable("local controller log source")
	}
	defer file.Close()
	follow := inv.Bool("follow")
	info, err := file.Stat()
	if err != nil {
		return err
	}

	emit := func(line dashboardLogLine) error {
		if mode == "json" {
			return json.NewEncoder(out).Encode(struct {
				Kind      string `json:"kind"`
				Tool      string `json:"tool"`
				Service   string `json:"service"`
				Line      string `json:"line"`
				Truncated bool   `json:"truncated,omitempty"`
			}{"log", "sparkwing", "dashboard", line.Text, line.Truncated})
		}
		if line.Truncated {
			line.Text += " … [line truncated]"
		}
		_, e := fmt.Fprintln(out, line.Text)
		return e
	}
	lines, pending, err := dashboardLogTail(ctx, file, info.Size(), limit, follow)
	if err != nil {
		return err
	}
	for _, line := range lines {
		if err = emit(line); err != nil {
			return err
		}
	}
	if !follow {
		return nil
	}
	if _, err = file.Seek(info.Size(), io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(file, 16*1024)

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		fragment, e := reader.ReadSlice('\n')
		appendDashboardLog(&pending, bytes.TrimSuffix(fragment, []byte{'\n'}))
		if e == nil {
			if err = emit(pending); err != nil {
				return err
			}
			pending = dashboardLogLine{}
		}
		if errors.Is(e, bufio.ErrBufferFull) {
			continue
		}
		if e != nil && !errors.Is(e, io.EOF) {
			return e
		}
		if errors.Is(e, io.EOF) {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		}
	}
}

type dashboardLogLine struct {
	Text      string
	Truncated bool
}

func appendDashboardLog(line *dashboardLogLine, fragment []byte) {
	remaining := 16*1024 - len(line.Text)
	take := len(fragment)
	if take > remaining {
		take = remaining
		line.Truncated = true
	}
	line.Text += string(fragment[:take])
}

func dashboardLogTail(ctx context.Context, file *os.File, size int64, limit int, follow bool) ([]dashboardLogLine, dashboardLogLine, error) {
	pending := dashboardLogLine{}
	if limit == 0 || size == 0 {
		return nil, pending, nil
	}
	start := max(int64(0), size-1024*1024)
	data, err := io.ReadAll(io.NewSectionReader(file, start, size-start))
	if err != nil {
		return nil, pending, err
	}
	if len(data) == 0 {
		return nil, pending, nil
	}
	parts := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
	prefixOmitted := start > 0
	if prefixOmitted && len(parts) > 1 {
		parts = parts[1:]
		prefixOmitted = false
		if len(parts) < limit {
			return nil, pending, errors.New("requested log history exceeds 1 MiB; reduce --tail or inspect the log file")
		}
	}
	if len(parts) > limit {
		parts = parts[len(parts)-limit:]
		prefixOmitted = false
	}
	lines := make([]dashboardLogLine, 0, len(parts))
	for i, part := range parts {
		if err = ctx.Err(); err != nil {
			return nil, pending, err
		}
		line := dashboardLogLine{}
		if i == 0 && prefixOmitted {
			appendDashboardLog(&line, []byte("[earlier line bytes omitted] "))
			line.Truncated = true
		}
		appendDashboardLog(&line, part)
		if follow && i == len(parts)-1 && !bytes.HasSuffix(data, []byte{'\n'}) {
			pending = line
		} else {
			lines = append(lines, line)
		}
	}
	return lines, pending, nil
}

func dashboardLogLimit(inv clisurface.Invocation) (int, error) {
	if !inv.Has("tail") {
		return 40, nil
	}
	limit, err := strconv.Atoi(inv.Value("tail"))
	if err != nil || limit < 0 {
		return 0, &clisurface.UsageError{Path: inv.Command.Path, Message: "--tail must be nonnegative"}
	}
	return limit, nil
}
