package nodehost

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

const hostExecSecret = "hostexec-secret-value"

func TestHostCommandMasksBeforeOutputCuts(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		for _, capture := range []bool{false, true} {
			t.Run(stream+"/capture="+strconv.FormatBool(capture), func(t *testing.T) {
				masker := secrets.NewMasker()
				if err := masker.Register(hostExecSecret); err != nil {
					t.Fatal(err)
				}
				// safety: the secret crosses the log's partial-line flush and the raw diagnostic tail cut.
				raw := strings.Repeat("x", (1<<20)-1) + hostExecSecret + strings.Repeat("y", nodeproto.ExecTailBytes-1)
				dir := t.TempDir()
				path := filepath.Join(dir, "output")
				if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
					t.Fatal(err)
				}
				var messages, captured strings.Builder
				logs := &planningLogs{masker: masker, known: true, sink: func(_ context.Context, frame nodeproto.FrameLog) error {
					messages.WriteString(frame.Message)
					return nil
				}}
				defer func() {
					if err := logs.finish(t.Context()); err != nil {
						t.Fatal(err)
					}
				}()
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				req := nodeproto.ExecRequest{ID: "boundary", Argv: []string{executable, "-test.run=^TestHostCommandOutputHelper$", "--", "--host-command-output", stream, path}, Dir: dir, Env: os.Environ(), Capture: capture}
				done, session := runHostCommand(req, masker, make(chan struct{}), make(chan struct{}), func(gotStream string, chunk []byte, eof bool) error {
					if capture {
						captured.Write(chunk)
						return nil
					}
					return logs.stream(t.Context(), gotStream, nodeproto.FrameLog{Level: "info"}, chunk, eof)
				}, nil)
				if err := endCommandSessions([]*commandSession{session}); err != nil {
					t.Fatal(err)
				}
				if err := logs.finish(t.Context()); err != nil {
					t.Fatal(err)
				}
				if done.ExitCode != 0 {
					t.Fatalf("command exit = %d", done.ExitCode)
				}
				if capture {
					if captured.String() != raw || len(done.Stdout)+len(done.Stderr) != 0 {
						t.Fatal("Capture did not preserve raw output without a diagnostic tail")
					}
					return
				}
				tail := done.Stdout
				if stream == "stderr" {
					tail = done.Stderr
				}
				masked := masker.Mask(raw)
				if string(tail) != masked[len(masked)-nodeproto.ExecTailBytes:] {
					t.Fatal("diagnostic tail was cut before masking")
				}
				if messages.String() != masked {
					t.Fatal("job log lost bytes or exposed a secret across an output boundary")
				}
			})
		}
	}
}

func TestHostCommandOutputHelper(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "--host-command-output" {
		return
	}
	file, err := os.Open(os.Args[len(os.Args)-1])
	if err != nil {
		os.Exit(10)
	}
	out := os.Stdout
	if os.Args[len(os.Args)-2] == "stderr" {
		out = os.Stderr
	}
	if _, err := io.Copy(out, file); err != nil {
		os.Exit(11)
	}
	_ = file.Close()
	os.Exit(0)
}
