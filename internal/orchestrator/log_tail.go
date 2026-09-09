package orchestrator

import (
	"errors"
	"io"
	"os"
)

func readLocalLog(f *os.File, opts LogsOpts) ([]byte, error) {
	// perf: filtering and merged streams need their original input, but an
	// unfiltered tail only needs the suffix containing its requested lines.
	if opts.Tail > 0 && opts.Head == 0 && opts.Lines == "" && opts.Grep == "" && !opts.EventsOnly && !opts.Tree && !opts.Follow {
		info, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() && info.Size() > 0 {
			if err := seekLocalTail(f, info.Size(), opts.Tail); err != nil {
				return nil, err
			}
		}
	}
	return io.ReadAll(f)
}

func seekLocalTail(f *os.File, size int64, lines int) error {
	const blockSize = 32 * 1024
	block := make([]byte, blockSize)
	end := size
	for end > 0 {
		start := max(int64(0), end-blockSize)
		chunk := block[:end-start]
		if _, err := f.ReadAt(chunk, start); err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			// safety: a truncation invalidates the snapshot offsets; read the file
			// from its current beginning rather than selecting an incomplete suffix.
			_, err = f.Seek(0, io.SeekStart)
			return err
		}
		for i := len(chunk) - 1; i >= 0; i-- {
			// safety: a final newline terminates the last line; it is not an extra line.
			if chunk[i] != '\n' || start+int64(i) == size-1 {
				continue
			}
			lines--
			if lines == 0 {
				_, err := f.Seek(start+int64(i)+1, io.SeekStart)
				return err
			}
		}
		end = start
	}
	_, err := f.Seek(0, io.SeekStart)
	return err
}
