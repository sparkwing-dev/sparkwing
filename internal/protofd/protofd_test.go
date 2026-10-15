package protofd_test

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/protofd"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == protofd.Arg {
		if err := exchange(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func exchange() error {
	in, out, err := protofd.Pipes()
	if err != nil {
		return err
	}
	for _, pipe := range []*os.File{in, out} {
		inheritable, err := pipeInheritable(pipe)
		if err != nil {
			return err
		}
		if inheritable {
			return fmt.Errorf("protocol pipe remains inheritable")
		}
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil {
		return err
	}
	if _, err := fmt.Fprint(os.Stdout, "user stdout\n"); err != nil {
		return err
	}
	_, err = fmt.Fprint(out, "reply: "+line)
	return err
}

func TestProtocolPipesArePrivateAndSeparateFromStdout(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	requestR, requestW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer requestR.Close()
	defer requestW.Close()
	frameR, frameW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer frameR.Close()
	defer frameW.Close()
	command := exec.CommandContext(t.Context(), executable, protofd.Arg)
	if err := inheritPipes(command, requestR, frameW); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	_ = requestR.Close()
	_ = frameW.Close()
	if _, err := io.WriteString(requestW, "ping\n"); err != nil {
		t.Fatal(err)
	}
	_ = requestW.Close()
	frames, err := io.ReadAll(frameR)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("pipe fixture: %v: %s", err, stderr.String())
	}
	if string(frames) != "reply: ping\n" || stdout.String() != "user stdout\n" {
		t.Fatalf("frames=%q stdout=%q", frames, stdout.String())
	}
}
