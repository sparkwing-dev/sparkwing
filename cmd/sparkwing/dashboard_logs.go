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
	"strings"
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
	offset := int64(0)
	if *limit > 0 && info.Size() > 1024*1024 {
		offset = info.Size() - 1024*1024
		if _, err = file.Seek(offset, io.SeekStart); err != nil {
			return err
		}
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
	reader := bufio.NewReaderSize(file, 16*1024)
	if *limit == 0 {
		if _, err = file.Seek(0, io.SeekEnd); err != nil {
			return err
		}
		reader.Reset(file)
	} else {
		lines := []dashboardLogLine{}
		for {
			line, e := readDashboardLogLine(reader)
			if e != nil && !errors.Is(e, io.EOF) {
				return e
			}
			if offset > 0 {
				offset = 0
			} else if line.Text != "" || !errors.Is(e, io.EOF) {
				lines = append(lines, line)
				if len(lines) > *limit {
					lines = lines[1:]
				}
			}
			if errors.Is(e, io.EOF) {
				break
			}
		}
		for _, line := range lines {
			if err = emit(line); err != nil {
				return err
			}
		}
	}
	if !*follow {
		return nil
	}

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		line, e := readDashboardLogLine(reader)
		if line.Text != "" || !errors.Is(e, io.EOF) {
			if err = emit(line); err != nil {
				return err
			}
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

func readDashboardLogLine(reader *bufio.Reader) (dashboardLogLine, error) {
	result := dashboardLogLine{}
	for {
		fragment, err := reader.ReadSlice('\n')
		fragment = bytes.TrimSuffix(fragment, []byte("\n"))
		remaining := 16*1024 - len(result.Text)
		take := len(fragment)
		if take > remaining {
			take = remaining
			result.Truncated = true
		}
		result.Text += string(fragment[:take])
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		result.Text = strings.TrimSuffix(result.Text, "\n")
		return result, err
	}
}
