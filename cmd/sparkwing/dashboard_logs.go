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
	"os/signal"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func runDashboardLogs(args []string) error {
	fs := flag.NewFlagSet(cmdDashboardLogs.Path, flag.ContinueOnError)
	home := fs.String("home", "", "state directory")
	output := fs.StringP("output", "o", "", "pretty|json|plain")
	limit := fs.Int("limit", 40, "last lines; 0 skips history")
	follow := fs.Bool("follow", false, "follow appended lines until interrupted")
	if err := parseAndCheck(cmdDashboardLogs, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	mode, err := resolveOutputFormat(*output, fs.Name())
	if err != nil {
		return err
	}
	if *limit < 0 {
		return errors.New("--limit must be nonnegative")
	}
	dp, err := resolveDashboardPaths(*home)
	if err != nil {
		return err
	}
	file, err := fssecure.OpenPrivateConfig(dp.log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}

	emit := func(line dashboardLogLine) error {
		if mode == "json" {
			return json.NewEncoder(os.Stdout).Encode(struct {
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
		_, e := fmt.Fprintln(os.Stdout, line.Text)
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	lines, pending, err := dashboardLogTail(ctx, file, info.Size(), 0, *limit, *follow)
	if err != nil {
		return err
	}
	for _, line := range lines {
		if err = emit(line); err != nil {
			return err
		}
	}
	if !*follow {
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

func dashboardLogTail(ctx context.Context, file *os.File, size, minimum int64, limit int, follow bool) ([]dashboardLogLine, dashboardLogLine, error) {
	pending := dashboardLogLine{}
	if limit == 0 || minimum >= size {
		return nil, pending, nil
	}
	start := max(minimum, size-1024*1024)
	data, err := io.ReadAll(io.NewSectionReader(file, start, size-start))
	if err != nil {
		return nil, pending, err
	}
	if len(data) == 0 {
		return nil, pending, nil
	}
	parts := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
	prefixOmitted := start > minimum
	if prefixOmitted && len(parts) > 1 {
		parts = parts[1:]
		prefixOmitted = false
		if len(parts) < limit {
			return nil, pending, errors.New("requested log history exceeds 1 MiB; reduce --limit or inspect the log file")
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
